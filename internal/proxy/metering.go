// 计量接入(architecture 生命周期第 11 步):每请求一行 usage_logs,
// usage 来源优先级 = 上游 usage 帧 > 本地估算(estimated 置位);
// 成功/失败/客户端断连三态都记,断流按已转发部分估算(metering 契约)。
package proxy

import (
	"encoding/json"
	"fmt"
	"math"
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

// costBudgetExceededCode 请求级成本预算超限的错误码(402 quota_exceeded 类型族,
// 错误类型表零膨胀;预检拒绝与流中断言共用)。
const costBudgetExceededCode = "cost_budget_exceeded"

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
	// 直通扩展(protocol-native-passthrough;翻译路径恒零值)
	upstreamBody   []byte // 不换道类上游错误体(原样透传客户端,零归一改写)
	upstreamStatus int    // 配套状态码
	upstreamCT     string // 配套 Content-Type(缺省 application/json)
	nativePath     bool   // 本 attempt 实际走直通(缓存双存侧判定)
	nativeRespID   string // 直通 responses 成功响应 id(亲和回写载体)
	canonicalBody  []byte // 直通双存的 canonical 侧(可 nil=降级为跨协议不可命中)

	// 请求级成本预算(per-request-cost-budget):budget<=0 或价目缺失 = 闸整体旁路零开销。
	// 输入/输出各取「usage 帧真值优先、估算兜底」;逐帧估算有轻微保守高估(预算宁紧勿松)。
	budget              int64             // key 配置的单请求成本上界(credit)
	budgetPrices        *metering.Pricing // 模型价目快照(别名级,跨候选同价;nil=价目缺失旁路)
	budgetModel         string            // 估算编码用模型名(别名)
	budgetEstIn         int64             // 输入估算(canonical 序列化计数;失格请求为 0)
	budgetIn            int64             // 输入累计真值(usage 帧)
	budgetOut           int64             // 输出累计真值(usage 帧)
	budgetOutEstAtUsage int64             // 上次 usage 到达时的估算累计检查点
	outputAtUsage       int               // 配套输出文本位置，落账只估算其后的缺失部分
	priceSnapshot       *metering.Pricing
	priceSnapshotTaken  bool  // chat 入口即便无价目也固定结果；其他端点沿原完成时刻计价路径
	budgetOutEst        int64 // 输出逐帧估算累计(delta 文本/工具参数)
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

// budgetObserveUsage usage 帧真值并入预算累计(非零字段覆盖,与计量合并同缝)。
func (m *meterState) budgetObserveUsage(u protocol.Usage) {
	if u.PromptTokens > 0 {
		m.budgetIn = u.PromptTokens
	}
	if u.CompletionTokens > 0 {
		m.budgetOut = u.CompletionTokens
		m.budgetOutEstAtUsage = m.budgetOutEst
		m.outputAtUsage = m.output.Len()
	}
}

// budgetExceeded 累计成本是否已超单请求预算:缓存折扣不计(保守不低估),
// 真值与估算取先到者(usage 帧未到时估算先行,到达后以真值覆盖)。
// 累计真值到达后，以当时的估算为检查点继续累计后续增量，不能冻结流中成本。
func (m *meterState) budgetExceeded() bool {
	if m.budget <= 0 || m.budgetPrices == nil {
		return false
	}
	in := m.budgetIn
	if in <= 0 {
		in = m.budgetEstIn
	}
	out := m.budgetOut
	if out <= 0 {
		out = m.budgetOutEst
	}
	outTotal := uint64(out)
	if m.budgetOut > 0 {
		outTotal += uint64(m.budgetOutEst - m.budgetOutEstAtUsage)
	}
	cost, valid := budgetCost(uint64(in), outTotal, m.budgetPrices)
	return !valid || cost > m.budget
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
	if (estimated || m.class == failover.ClassCostBudget) && m.budgetOut > 0 && m.output.Len() > m.outputAtUsage {
		after := metering.EstimateOutputTokens(m.output.String()[m.outputAtUsage:], upstream)
		if after > math.MaxInt64-m.budgetOut {
			p.logger.Warn("计量累计输出超出整数范围，转为全文估算", "request_id", logging.RequestIDFrom(r.Context()))
			usage.CompletionTokens = metering.EstimateOutputTokens(m.output.String(), upstream)
		} else {
			usage.CompletionTokens = m.budgetOut + after
		}
		estimated = true
	}
	// 中断前的 usage 可能只有输入；按字段补齐，不能把“有任意帧”当成完整计量。
	if estimated || m.class == failover.ClassCostBudget {
		if usage.PromptTokens == 0 {
			usage.PromptTokens = estimateInputTokens(inputFallback, upstream)
			estimated = true
		}
		if usage.CompletionTokens == 0 && m.output.Len() > 0 {
			usage.CompletionTokens = metering.EstimateOutputTokens(m.output.String(), upstream)
			estimated = true
		}
	}

	tenantID, apiKeyID := "", ""
	if info := auth.KeyFrom(r.Context()); info != nil {
		tenantID, apiKeyID = info.TenantID, info.ID
	}
	pricing := m.priceSnapshot
	if !m.priceSnapshotTaken {
		if pt := p.pricing.Get(modelAlias); pt != nil {
			pr := pt.At(time.Now())
			pricing = &pr
		}
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
	row := metering.NewRow(metering.RowParams{
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
		Currency:      p.settlement,
		Pricing:       pricing,
		FirstTokenMs:  m.firstMs,
		TotalMs:       total,
	})
	if row.ErrorClass == metering.CostOverflowClass {
		p.logger.Error("计量金额不可表示，保留未结算观测行",
			"request_id", row.RequestID, "tenant_id", tenantID, "api_key_id", apiKeyID,
			"provider", cand.Provider, "model_alias", modelAlias, "currency", row.Currency,
			"prompt_tokens", usage.PromptTokens, "cached_tokens", usage.CachedTokens,
			"completion_tokens", usage.CompletionTokens, "pricing", pricing,
			"upstream_error_class", errorClassOf(m), "estimated", row.Estimated, "usage_estimated", estimated)
	}
	p.pipeline.Submit(row)
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
	case failover.ClassContextCanceled, failover.ClassCostBudget:
		// 客户端断连/预算中断(网关策略)非 key 质量问题:判定表「静默退出,不计失败」,不回写任何结果
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

// preflightBudgetExceeded 预检判定:预算>0 且价目可得且 max_tokens>0 时,
// 估算「输入成本 + max_tokens×输出价」上界(缓存折扣不计,保守不低估);
// 返回 (可读 message, 是否超限)。max_tokens 缺失 = 上界不可知,不伪保护(流式由中断闸兜底)。
func preflightBudgetExceeded(req *protocol.ChatRequest, budget int64, prices *metering.Pricing) (string, bool) {
	if req.MaxTokens == nil || *req.MaxTokens <= 0 {
		return "", false
	}
	if prices == nil {
		return "", false
	}
	inputText, _ := json.Marshal(req.Messages)
	in := estimateInputTokens(string(inputText), req.Model)
	cost, valid := budgetCost(uint64(in), uint64(*req.MaxTokens), prices)
	if !valid {
		return "单请求预估成本超出整数范围，已超过预算", true
	}
	if cost <= budget {
		return "", false
	}
	return fmt.Sprintf("单请求成本预算已超限(预估上界 %d credit > 预算 %d credit)", cost, budget), true
}

// accumulateDelta 从 SSE 载荷(适配器翻译帧)累计:content/reasoning 入估算 fallback,
// content 另入缓存拼包累计,tool_call 参数计入估算(纯工具流估算不归零),终态帧捕获 finish_reason。
// 解析失败静默跳过。
func (m *meterState) accumulateDelta(payload []byte) {
	chunk, err := protocol.ParseStreamChunk(payload)
	if err != nil {
		return
	}
	// 预算逐帧估算仅在闸启用时计数(零开销旁路);估算口径与落账估算一致(文本+工具参数)
	counting := m.budget > 0 && m.budgetPrices != nil
	var budgeted strings.Builder
	for _, ch := range chunk.Choices {
		if ch.Delta.Content != nil {
			m.output.WriteString(*ch.Delta.Content)
			m.contentOut.WriteString(*ch.Delta.Content)
			if counting {
				budgeted.WriteString(*ch.Delta.Content)
			}
		}
		if ch.Delta.ReasoningContent != nil {
			m.output.WriteString(*ch.Delta.ReasoningContent)
			if counting {
				budgeted.WriteString(*ch.Delta.ReasoningContent)
			}
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
					if counting {
						budgeted.WriteString(tc.Function.Arguments)
					}
				}
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			m.finishReason = *ch.FinishReason
		}
	}
	if counting && budgeted.Len() > 0 {
		m.budgetOutEst += metering.EstimateOutputTokens(budgeted.String(), m.budgetModel)
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

// budgetCost 使用宽乘积逐段 ceil，允许中间乘积大但最终 credit 可表示的合法上界。
// 最终成本超出 int64 时必定超过任何可配置预算，不能让回绕放行。
func budgetCost(in, out uint64, p *metering.Pricing) (int64, bool) {

	a, ok := metering.CostForTokens(in, p.InputCreditsPerM)
	if !ok {
		return 0, false
	}
	b, ok := metering.CostForTokens(out, p.OutputCreditsPerM)
	if !ok || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}
