// 计量接入(architecture 生命周期第 11 步):每请求一行 usage_logs,
// usage 来源优先级 = 上游 usage 帧 > 本地估算(estimated 置位);
// 成功/失败/客户端断连三态都记,断流按已转发部分估算(metering 契约)。
package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/protocol"
)

// meterState 单请求计量状态(随 attempt 生命周期演进,退出时统一落账)。
type meterState struct {
	start time.Time

	normalize    func(json.RawMessage) (protocol.Usage, error) // 适配器 usage 归一(attempt 注入)
	output       strings.Builder                               // delta 文本累计(估算 fallback,content+reasoning)
	contentOut   strings.Builder                               // 仅 content 增量(语义缓存拼包用,不含 reasoning)
	finishReason string                                        // 流式终态(判定表口径;供缓存准入 finish=stop 判定)
	est          bool                                          // 估算路径置位
	status       string                                        // ok | error | client_cancel
	class        failover.ErrorClass
	firstMs      *int64 // 首 token 延迟(开闸时刻)
	firstSet     bool
	stream       bool              // 流式与否(计量行 stream 列)
	retryAfter   time.Duration     // 上游 429 的 Retry-After(0=未提供)
	upstreamMsg  string            // 上游错误 body 中的人类可读 message(终结信封透传用)
	usageFrames  []json.RawMessage // 上游 usage 原始对象(可多帧:流式 start/delta 各一)
	outBody      []byte            // 成功响应体(非流式=改写产物;流式=拼包结果;缓存入库候选)
}

// markFirstToken 记录首 token 延迟(仅首个内容帧时生效)。
func (m *meterState) markFirstToken() {
	if m.firstSet {
		return
	}
	m.firstSet = true
	ms := time.Since(m.start).Milliseconds()
	m.firstMs = &ms
}

// record 第 11 步落账:构造行并投递计量管道(异步批量落库)。
// modelAlias/upstream 为对外别名与上游模型名;inputFallback 为估算路径的输入文本(chat=messages 序列化,embeddings=input)。
func (p *Proxy) record(r *http.Request, modelAlias, upstream string, cand failover.Candidate, inputFallback string, m *meterState) {
	if p.pipeline == nil {
		return
	}
	total := time.Since(m.start).Milliseconds()

	// usage 来源:上游 usage 帧(多帧字段合并) > 本地估算(metering 优先级契约)
	usage, estimated := protocol.Usage{}, m.est
	if len(m.usageFrames) > 0 && m.normalize != nil {
		for _, raw := range m.usageFrames {
			u, err := m.normalize(raw)
			if err != nil {
				p.logger.Warn("usage 归一失败,该帧跳过",
					"request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
				estimated = true
				continue
			}
			usage = mergeUsage(usage, u)
		}
	}
	if estimated && usage == (protocol.Usage{}) {
		usage = protocol.Usage{
			PromptTokens:     estimateInputTokens(inputFallback, upstream),
			CompletionTokens: metering.EstimateOutputTokens(m.output.String(), upstream),
		}
	}

	tenantID, apiKeyID := "", ""
	if info := auth.KeyFrom(r.Context()); info != nil {
		tenantID, apiKeyID = info.TenantID, info.ID
	}
	var pricing *metering.Pricing
	if pt := p.pricing.Get(modelAlias); pt != nil {
		// 分时段倍率:按当前时刻选窗(加载期已换算为整数有效价,此处零浮点查表);
		// 目录由管理面改价后原子替换,本请求读到的是当前快照
		pr := pt.At(time.Now())
		pricing = &pr
	}

	UpstreamTotal.Inc(cand.Provider, errorClassOf(m))

	// token 流向序列:与落账行同口径(含估算行;reasoning 计入 output 段)
	observeTokens(cand.Provider, modelAlias, usage)
	// 流式首 token 延迟:仅流式产生过首 token 的请求观测(非流式总延迟已有 RequestDuration)
	if m.firstMs != nil {
		FirstTokenDuration.Observe(float64(*m.firstMs))
	}

	// TPM 记账:请求完成后按实际 token 计入滑动窗口(traffic-governance 契约)
	if p.tpm != nil {
		if info := auth.KeyFrom(r.Context()); info != nil {
			p.tpm.Record(info.ID, usage.PromptTokens+usage.CompletionTokens, time.Now())
		}
	}

	RequestsTotal.Inc(modelAlias, m.status)
	RequestDuration.Observe(float64(total))
	p.pipeline.Submit(metering.NewRow(metering.RowParams{
		RequestID:     logging.RequestIDFrom(r.Context()),
		TenantID:      tenantID,
		APIKeyID:      apiKeyID,
		Provider:      cand.Provider,
		ModelAlias:    modelAlias,
		UpstreamModel: upstream,
		Stream:        m.stream,
		Status:        m.status,
		ErrorClass:    errorClassOf(m),
		Usage:         usage,
		Estimated:     estimated,
		Pricing:       pricing,
		FirstTokenMs:  m.firstMs,
		TotalMs:       total,
	}))
}

// reportKeyOutcome 第 9 步收尾:按错误分类把结果回写 key 池(冷却/禁用/EMA)。
func (p *Proxy) reportKeyOutcome(pool *keypool.Pool, key *keypool.Key, class failover.ErrorClass, handled bool, m *meterState) {
	now := time.Now()
	// 成功:状态 ok 且无错误类
	if handled && class == "" && m.status == "ok" {
		pool.Report(key, keypool.OutcomeSuccess, now, 0)
		return
	}
	switch class {
	case failover.ClassContextCanceled:
		// 客户端主动断连非 key 质量问题:判定表「静默退出,不计失败」,不回写任何结果
		return
	case failover.ClassRateLimited:
		pool.Report(key, keypool.OutcomeRateLimited, now, m.retryAfter)
	case failover.ClassAuthFailed:
		pool.Report(key, keypool.OutcomeAuthFailed, now, 0)
	case failover.ClassUpstream5xx:
		pool.Report(key, keypool.OutcomeServerError, now, 0)
	default:
		// dial/timeout/mid_stream/unknown/内部类:只记 EMA 失败,不触发冷却计数
		pool.Report(key, keypool.OutcomeFailure, now, 0)
	}
}

// mergeUsage usage 帧合并:非零字段覆盖(桥接流式 input/output 分帧到达)。
func mergeUsage(base, next protocol.Usage) protocol.Usage {
	if v := next.PromptTokens; v != 0 {
		base.PromptTokens = v
	}
	if v := next.CompletionTokens; v != 0 {
		base.CompletionTokens = v
	}
	if v := next.ReasoningTokens; v != 0 {
		base.ReasoningTokens = v
	}
	if v := next.CachedTokens; v != 0 {
		base.CachedTokens = v
	}
	return base
}

// errorClassOf 错误类列:仅失败请求记,成功/断连不记。
func errorClassOf(m *meterState) string {
	if m.status == "error" && m.class != "" {
		return string(m.class)
	}
	return ""
}

// estimateInputTokens 输入估算:入站文本计数(metering 契约)。
func estimateInputTokens(inputText, model string) int64 {
	n, _ := metering.CountTokens(inputText, model)
	return n
}

// accumulateDelta 从 SSE 载荷(适配器翻译帧)累计:content/reasoning 入估算 fallback,
// content 另入缓存拼包累计,tool_call 参数计入估算(纯工具流估算不归零),终态帧捕获 finish_reason。
// 解析失败静默跳过。
func (m *meterState) accumulateDelta(payload []byte) {
	chunk, err := protocol.ParseStreamChunk(payload)
	if err != nil {
		return
	}
	for _, ch := range chunk.Choices {
		if ch.Delta.Content != nil {
			m.output.WriteString(*ch.Delta.Content)
			m.contentOut.WriteString(*ch.Delta.Content)
		}
		if ch.Delta.ReasoningContent != nil {
			m.output.WriteString(*ch.Delta.ReasoningContent)
		}
		for _, raws := range [][]json.RawMessage{ch.Delta.ToolCalls, ch.ToolCalls} {
			for _, rc := range raws {
				var tc struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				}
				if json.Unmarshal(rc, &tc) == nil {
					m.output.WriteString(tc.Function.Arguments)
				}
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			m.finishReason = *ch.FinishReason
		}
	}
}

// assembleStreamBody 流式成功后的缓存入库候选:按累计 content 拼标准 chat.completion 体。
// 仅 finish_reason=stop 且内容非空才产出(其余形态交给 ResponseAdmissible 的准入语义,宁缺勿错)。
func (m *meterState) assembleStreamBody(upstreamModel string) []byte {
	if m.finishReason != "stop" {
		return nil
	}
	content := m.contentOut.String()
	if content == "" {
		return nil
	}
	body := map[string]any{
		"object": "chat.completion",
		"model":  upstreamModel,
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
	}
	if u := m.mergedUsage(); u != (protocol.Usage{}) {
		body["usage"] = u
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	return b
}

// mergedUsage 多 usage 帧归一合并(非零覆盖);归一失败帧跳过。
func (m *meterState) mergedUsage() protocol.Usage {
	usage := protocol.Usage{}
	if m.normalize == nil {
		return usage
	}
	for _, raw := range m.usageFrames {
		if u, err := m.normalize(raw); err == nil {
			usage = mergeUsage(usage, u)
		}
	}
	return usage
}
