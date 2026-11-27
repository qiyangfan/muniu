// 计量接入：每请求一行 usage_logs，usage 来源优先级为上游 usage 帧、其次本地估算并置位 estimated。
// 成功、失败、客户端断连三态都记，断流按已转发部分估算。
package proxy

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/metrics"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/ratelimit"
)

// costBudgetExceededCode 请求级成本预算超限的错误码，归入 402 quota_exceeded 类型族，
// 不新增错误类型；预检拒绝与流中断言共用。
const costBudgetExceededCode = "cost_budget_exceeded"

// meterState 单请求计量状态，随 attempt 生命周期演进，退出时统一落账。
type meterState struct {
	start time.Time

	normalize    func(json.RawMessage) (protocol.Usage, error) // 适配器 usage 归一，attempt 注入
	output       strings.Builder                               // delta 文本累计，估算 fallback 用，content+reasoning
	contentOut   strings.Builder                               // 仅 content 增量，语义缓存拼包用，不含 reasoning
	finishReason string                                        // 流式终态，判定表口径，供缓存准入 finish=stop 判定
	est          bool                                          // 估算路径置位
	status       string                                        // ok | error | client_cancel
	class        failover.ErrorClass
	firstMs      *int64 // 首 token 延迟，开闸时刻
	firstSet     bool
	stream       bool          // 流式与否，计量行 stream 列
	retryAfter   time.Duration // 上游 429 的 Retry-After，0 表示未提供
	// creditEvidence 欠费证据（封闭白名单，attempt 非 2xx 分支采集）；上报映射到欠费桶，
	// 换道语义不变（判定表已按原错误类走完），只改池的跨请求记忆。文案永不参与判定。
	creditEvidence bool
	upstreamMsg    string            // 上游错误 body 中的人类可读 message，终结信封透传用
	usageFrames    []json.RawMessage // 上游 usage 原始对象，可多帧，流式 start/delta 各一
	outBody        []byte            // 成功响应体，非流式为改写产物、流式为拼包结果，缓存入库候选
	// 直通路径专用字段，翻译路径恒零值
	upstreamBody    []byte // 不换道类上游错误体，原样透传客户端，零归一改写
	upstreamStatus  int    // 配套状态码
	upstreamCT      string // 配套 Content-Type，缺省 application/json
	upstreamHeaders http.Header
	nativePath      bool   // 本 attempt 实际走直通，缓存双存侧判定
	nativeRespID    string // 直通 responses 成功响应 id，亲和回写载体
	canonicalBody   []byte // 直通双存的 canonical 侧，可 nil，降级为跨协议不可命中

	// 请求级成本预算：budget<=0 或价目缺失时闸整体旁路零开销。
	// 输入/输出各取 usage 帧真值优先、估算兜底；逐帧估算有轻微保守高估，预算宁紧勿松。
	budget              int64             // key 配置的单请求成本上界(credit)
	budgetPrices        *metering.Pricing // 模型价目快照，别名级，跨候选同价；nil 表示价目缺失旁路
	budgetModel         string            // 估算编码用模型名，即别名
	budgetEstIn         int64             // 输入估算，canonical 序列化计数，失格请求为 0
	budgetIn            int64             // 输入累计真值，来自 usage 帧
	budgetOut           int64             // 输出累计真值，来自 usage 帧
	budgetOutEstAtUsage int64             // 上次 usage 到达时的估算累计检查点
	outputAtUsage       int               // 配套输出文本位置，落账只估算其后的缺失部分
	priceSnapshot       *metering.Pricing
	priceSnapshotTaken  bool  // chat 入口即便无价目也固定结果；其他端点沿原完成时刻计价路径
	budgetOutEst        int64 // 输出逐帧估算累计，含 delta 文本与工具参数
}

// markFirstToken 记录首 token 延迟，仅首个内容帧时生效。
func (m *meterState) markFirstToken() {
	if m.firstSet {
		return
	}
	m.firstSet = true
	ms := time.Since(m.start).Milliseconds()
	m.firstMs = &ms
}

// budgetObserveUsage 将 usage 帧真值并入预算累计，非零字段覆盖，与计量合并同口径。
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

// budgetExceeded 判断累计成本是否已超单请求预算：缓存折扣不计，保守不低估。
// 真值与估算取先到者，usage 帧未到时估算先行，到达后以真值覆盖。
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
	outTotal := toU64(out)
	if m.budgetOut > 0 {
		outTotal += toU64(m.budgetOutEst - m.budgetOutEstAtUsage)
	}
	cost, valid := budgetCost(toU64(in), outTotal, m.budgetPrices)
	return !valid || cost > m.budget
}

// toU64 负数按 0 处理：预算路径不接受回绕出的巨大值。
func toU64(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// record 落账：构造行并投递计量管道，异步批量落库。
// modelAlias/upstream 为对外别名与上游模型名；inputFallback 为估算路径的输入文本，chat 取 messages 序列化，embeddings 取 input。
func (p *Proxy) record(r *http.Request, modelAlias, upstream string, cand failover.Candidate,
	inputFallback string, m *meterState) {
	if p.pipeline == nil {
		return
	}
	total := time.Since(m.start).Milliseconds()
	usage, estimated := p.mergeUsageFrames(r, m, upstream, inputFallback)
	tenantID, apiKeyID := meterIdentity(r)
	pricing := m.pricingSnapshot(p.pricing, modelAlias)

	UpstreamTotal.Inc(cand.Provider, errorClassOf(m))

	// token 流向序列与落账行同口径，含估算行，reasoning 计入 output 段
	observeTokens(cand.Provider, modelAlias, usage)
	// 流式首 token 延迟：仅流式产生过首 token 的请求观测，非流式总延迟已有 RequestDuration
	if m.firstMs != nil {
		FirstTokenDuration.Observe(float64(*m.firstMs))
	}
	p.recordTPM(r, usage)

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

// mergeUsageFrames usage 来源：上游 usage 帧多帧字段合并，优先于本地估算；
// 估算或预算中断时按字段补齐，不能把“有任意帧”当成完整计量。
func (p *Proxy) mergeUsageFrames(r *http.Request, m *meterState,
	upstream, inputFallback string) (protocol.Usage, bool) {
	usage, estimated := protocol.Usage{}, m.est
	if len(m.usageFrames) > 0 && m.normalize != nil {
		for _, raw := range m.usageFrames {
			u, err := m.normalize(raw)
			usage = mergeUsage(usage, u)
			if err != nil {
				p.logger.Warn("usage 归一失败,保留可确认字段并标记估算",
					"request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
				estimated = true
				continue
			}
		}
	}
	usage, estimated = m.estimateAfterUsage(usage, estimated, upstream)
	if estimated || m.class == failover.ClassCostBudget {
		usage, estimated = fillMissingUsage(usage, estimated, inputFallback, upstream, m.output.String())
	}
	return usage, estimated
}

// estimateAfterUsage 累计真值到达后以当时的估算为检查点继续累计后续增量，不能冻结流中成本。
func (m *meterState) estimateAfterUsage(usage protocol.Usage, estimated bool, upstream string) (protocol.Usage, bool) {
	if (!estimated && m.class != failover.ClassCostBudget) || m.budgetOut <= 0 || m.output.Len() <= m.outputAtUsage {
		return usage, estimated
	}
	after := metering.EstimateOutputTokens(m.output.String()[m.outputAtUsage:], upstream)
	if after > math.MaxInt64-m.budgetOut {
		usage.CompletionTokens = metering.EstimateOutputTokens(m.output.String(), upstream)
	} else {
		usage.CompletionTokens = m.budgetOut + after
	}
	return usage, true
}

// fillMissingUsage 中断前的 usage 可能只有输入；按字段补齐缺失侧。
func fillMissingUsage(usage protocol.Usage, estimated bool, inputFallback, upstream,
	outText string) (protocol.Usage, bool) {
	if usage.PromptTokens == 0 {
		usage.PromptTokens = estimateInputTokens(inputFallback, upstream)
		estimated = true
	}
	if usage.CompletionTokens == 0 && len(outText) > 0 {
		usage.CompletionTokens = metering.EstimateOutputTokens(outText, upstream)
		estimated = true
	}
	return usage, estimated
}

// meterIdentity 从 ctx 取租户与 key 归属，缺失按空串。
func meterIdentity(r *http.Request) (tenantID, apiKeyID string) {
	if info := auth.KeyFrom(r.Context()); info != nil {
		return info.TenantID, info.ID
	}
	return "", ""
}

// pricingSnapshot 价目快照：chat 入口固定，其他端点沿原完成时刻计价路径。
func (m *meterState) pricingSnapshot(dir *metering.PricingDirectory, modelAlias string) *metering.Pricing {
	if m.priceSnapshotTaken {
		return m.priceSnapshot
	}
	if pt := dir.Get(modelAlias); pt != nil {
		pr := pt.At(time.Now())
		return &pr
	}
	return nil
}

// recordTPM TPM 记账：请求完成后按实际 token 计入 key 与租户聚合滑动窗口。
func (p *Proxy) recordTPM(r *http.Request, usage protocol.Usage) {
	if p.tpm == nil {
		return
	}
	if info := auth.KeyFrom(r.Context()); info != nil {
		tokens := usage.PromptTokens + usage.CompletionTokens
		now := time.Now()
		p.tpm.Record(info.ID, tokens, now)
		if tenantRec, ok := p.tpm.(ratelimit.TenantTPMRecorder); ok {
			tenantRec.RecordTenant(info.TenantID, tokens, now)
		}
	}
}

// reportKeyOutcome 收尾：按错误分类把结果回写 key 池，触发冷却、禁用或 EMA 更新。
func (p *Proxy) reportKeyOutcome(pool *keypool.Pool, key *keypool.Key,
	class failover.ErrorClass, handled bool, m *meterState) {
	now := time.Now()
	// 成功：状态 ok 且无错误类
	if handled && class == "" && m.status == "ok" {
		pool.Report(key, keypool.OutcomeSuccess, now, 0)
		return
	}
	switch class {
	case failover.ClassContextCanceled, failover.ClassCostBudget:
		// 客户端断连与预算中断属网关策略，非 key 质量问题：静默退出不计失败，不回写任何结果
		return
	}
	if m.creditEvidence {
		reportCreditExhausted(pool, key, now)
		return
	}
	switch class {
	case failover.ClassRateLimited:
		pool.Report(key, keypool.OutcomeRateLimited, now, m.retryAfter)
	case failover.ClassAuthFailed:
		pool.Report(key, keypool.OutcomeAuthFailed, now, 0)
	case failover.ClassUpstream5xx:
		pool.Report(key, keypool.OutcomeServerError, now, 0)
	default:
		// dial/timeout/mid_stream/unknown/内部类只记 EMA 失败，不触发冷却计数
		pool.Report(key, keypool.OutcomeFailure, now, 0)
	}
}

// reportCreditExhausted 欠费证据（封闭白名单）→ 欠费桶：只改池的跨请求记忆，换道语义不变
// （判定表已按原错误类走完）。计入欠费桶事件指标供余额回写面对照。
func reportCreditExhausted(pool *keypool.Pool, key *keypool.Key, now time.Time) {
	CreditEvents.Inc("upstream", "enter")
	pool.Report(key, keypool.OutcomeCreditExhausted, now, 0)
}

// isCreditExhausted 欠费证据封闭白名单：HTTP 402，或错误体错误码/type 属 insufficient_quota
// （OpenAI 兼容族通用形态，顶层与 error 嵌套两种摆位都认）。刻意不做错误文案模糊匹配——
// 厂商方言逐版本漂移是竞品的维护包袱；白名单外的一切失败维持既有分类。
func isCreditExhausted(status int, body []byte) bool {
	if status == http.StatusPaymentRequired {
		return true
	}
	var probe struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
		Type string `json:"type"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Error.Type == "insufficient_quota" ||
		probe.Error.Code == "insufficient_quota" ||
		probe.Type == "insufficient_quota" ||
		probe.Code == "insufficient_quota"
}

// CreditEvents 欠费桶事件计数：cause=upstream|balance，action=enter|release。
// 装配层 balance 回写侧同用（balance 因由）。
var CreditEvents = metrics.NewCounterVec("muniu_keypool_credit_events_total",
	"欠费桶事件计数(进入/提前释放)", "cause", "action")

// mergeUsage 合并 usage 帧，非零字段覆盖，桥接流式的 input/output 分帧到达。
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
	if v := next.VisionTokens; v != 0 {
		base.VisionTokens = v
	}
	return base
}

// errorClassOf 错误类列：仅失败请求记录，成功与断连不记。
func errorClassOf(m *meterState) string {
	if m.status == "error" && m.class != "" {
		return string(m.class)
	}
	return ""
}

// estimateInputTokens 输入估算：按入站文本计数。
func estimateInputTokens(inputText, model string) int64 {
	n, _ := metering.CountTokens(inputText, model)
	return n
}

// preflightBudgetExceeded 预检判定：预算>0 且价目可得且 max_tokens>0 时，
// 估算输入成本加 max_tokens×输出价的上界，缓存折扣不计，保守不低估。
// 返回可读 message 与是否超限。max_tokens 缺失即上界不可知，不做伪保护，流式由中断闸兜底。
func preflightBudgetExceeded(req *protocol.ChatRequest, budget int64, prices *metering.Pricing) (string, bool) {
	if req.MaxTokens == nil || *req.MaxTokens <= 0 {
		return "", false
	}
	if prices == nil {
		return "", false
	}
	inputText, _ := json.Marshal(req.Messages)
	in := estimateInputTokens(string(inputText), req.Model)
	cost, valid := budgetCost(toU64(in), toU64(*req.MaxTokens), prices)
	if !valid {
		return "单请求预估成本超出整数范围，已超过预算", true
	}
	if cost <= budget {
		return "", false
	}
	return fmt.Sprintf("单请求成本预算已超限(预估上界 %d credit > 预算 %d credit)", cost, budget), true
}

// accumulateDelta 从 SSE 载荷即适配器翻译帧累计：content/reasoning 入估算 fallback，
// content 另入缓存拼包累计，tool_call 参数计入估算，纯工具流估算不归零，终态帧捕获 finish_reason。
// 解析失败静默跳过。
func (m *meterState) accumulateDelta(payload []byte) {
	chunk, err := protocol.ParseStreamChunk(payload)
	if err != nil {
		return
	}
	// 预算逐帧估算仅在闸启用时计数，零开销旁路；估算口径与落账估算一致，含文本与工具参数
	counting := m.budget > 0 && m.budgetPrices != nil
	var budgeted strings.Builder
	for _, ch := range chunk.Choices {
		m.absorbChoiceDelta(ch, counting, &budgeted)
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			m.finishReason = *ch.FinishReason
		}
	}
	if counting && budgeted.Len() > 0 {
		m.budgetOutEst += metering.EstimateOutputTokens(budgeted.String(), m.budgetModel)
	}
}

// absorbChoiceDelta 单条 choice 的增量累计：content/reasoning 与工具参数分别入文本与预算缓冲。
func (m *meterState) absorbChoiceDelta(ch protocol.StreamChoice, counting bool, budgeted *strings.Builder) {
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
		m.absorbToolArgs(raws, counting, budgeted)
	}
}

// absorbToolArgs 工具调用参数计入输出文本与预算估算：纯工具流无正文时估算不归零。
func (m *meterState) absorbToolArgs(raws []json.RawMessage, counting bool, budgeted *strings.Builder) {
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

// absorbStreamUsage usage 帧：抽内层 usage 对象供归一，多帧合并适配桥接 start/delta 分离。
func (m *meterState) absorbStreamUsage(src []byte, meta adapter.EventMeta) {
	if !meta.IsUsage {
		return
	}
	var frame struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(src, &frame); err != nil || len(frame.Usage) == 0 || string(frame.Usage) == "null" {
		return
	}
	m.usageFrames = append(m.usageFrames, frame.Usage)
	// 预算真值并入，真值覆盖估算，归一失败不影响计量主路径
	if m.budget > 0 && m.normalize != nil {
		if u, uerr := m.normalize(frame.Usage); uerr == nil {
			m.budgetObserveUsage(u)
		}
	}
}

// noteUsageMissing usage 缺失即置估算位，落账走本地估算路径。
func (m *meterState) noteUsageMissing() {
	if len(m.usageFrames) == 0 {
		m.est = true
	}
}

// assembleStreamBody 流式成功后的缓存入库候选：按累计 content 拼标准 chat.completion 体。
// 仅 finish_reason=stop 且内容非空才产出，其余形态交给 ResponseAdmissible 的准入语义，宁缺勿错。
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

// mergedUsage 多 usage 帧归一合并，非零覆盖；归一失败帧跳过。
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

// cacheReadSum 缓存读证据合计(worth 闸门):跨 usage 帧的方言键扫描,任一非零即证据。
func (m *meterState) cacheReadSum() int64 {
	total := int64(0)
	for _, f := range m.usageFrames {
		total += extractCacheRead(f)
	}
	return total
}
