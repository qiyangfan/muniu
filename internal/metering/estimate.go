// 本地估算路径：上游 usage 缺失或断流中断时，输入按请求 messages 序列化后的 tiktoken 计数，
// 输出按已转发 delta 文本累计计数，一律置 estimated=true；reasoning/cached 估算不到就置 0，不猜。
// 词表经 tiktoken-go-loader OfflineLoader 编译期嵌入，运行时零外联。
package metering

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
	loader "github.com/pkoukk/tiktoken-go-loader"

	"github.com/solvela/muniu/internal/protocol"
)

func init() {
	// 词表随二进制发布，严禁运行时下载。
	tiktoken.SetBpeLoader(&loader.OfflineLoader{})
}

var (
	encMu       sync.Mutex
	encCache    = map[string]*tiktoken.Tiktoken{}
	encodingFor = defaultEncodingFor // 模型前缀 → 编码名；抽成变量便于测试替换。
)

// defaultEncodingFor 编码映射：已知前缀显式映射，未知模型缺省 o200k_base。
func defaultEncodingFor(model string) string {
	switch {
	// o200k 系前缀，须先于 gpt-4 判定。
	case strings.HasPrefix(model, "gpt-4o"),
		strings.HasPrefix(model, "gpt-4.1"),
		strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"), strings.HasPrefix(model, "o4"):
		return "o200k_base"
	case strings.HasPrefix(model, "gpt-4"), strings.HasPrefix(model, "gpt-3.5"):
		return "cl100k_base"
	default:
		// deepseek/glm/qwen 等国产系与未知模型在 openai 无对应词表，统一用 o200k_base 近似。
		return "o200k_base"
	}
}

// getEncoding 返回编码实例，进程内缓存；tiktoken 实例并发安全。
func getEncoding(model string) (*tiktoken.Tiktoken, error) {
	name := encodingFor(model)
	encMu.Lock()
	defer encMu.Unlock()
	if enc, ok := encCache[name]; ok {
		return enc, nil
	}
	enc, err := tiktoken.GetEncoding(name)
	if err != nil {
		return nil, fmt.Errorf("metering: 加载编码 %s: %w", name, err)
	}
	encCache[name] = enc
	return enc, nil
}

// CountTokens 文本 token 计数，按 BPE 精确计数而非粗估。
func CountTokens(text, model string) (int64, error) {
	if text == "" {
		return 0, nil
	}
	enc, err := getEncoding(model)
	if err != nil {
		return 0, err
	}
	return int64(len(enc.Encode(text, nil, nil))), nil
}

// estimateInputTokens 入站 messages 序列化后计数，是计价口径的近似。
// 仅包内测试消费；生产估算路径走 proxy 层对已序列化文本的 CountTokens，勿再导出第二入口。
func estimateInputTokens(messages []map[string]any, model string) int64 {
	b, err := json.Marshal(messages)
	if err != nil {
		return 0
	}
	n, _ := CountTokens(string(b), model)
	return n
}

// EstimateOutputTokens 已转发 delta 文本累计计数。
func EstimateOutputTokens(text, model string) int64 {
	n, _ := CountTokens(text, model)
	return n
}

// Pricing 单别名价目，μ/百万Token 经 ParsePrice 换算后的整数形态。
// CachedInputCreditsPerM=0 表示缓存命中与输入同价。
type Pricing struct {
	Currency               string
	InputCreditsPerM       int64
	CachedInputCreditsPerM int64
	OutputCreditsPerM      int64
}

// RowParams 落库行参数，供 NewRow 消费；字段语义与 usage_logs DDL 一一对应。
type RowParams struct {
	RequestID     string
	TenantID      string
	APIKeyID      string
	Provider      string
	ModelAlias    string
	UpstreamModel string
	Stream        bool
	Status        string // ok | error | client_cancel
	ErrorClass    string // 可空
	Usage         protocol.Usage
	Estimated     bool
	Pricing       *Pricing // nil 则成本记 0，如估算路径未接价目。
	Currency      string   // 非空则覆盖价目币种，语义缓存命中行专用。
	FirstTokenMs  *int64
	TotalMs       int64
}

// CostOverflowClass 标记无法按 int64 表达费用的未结算观测，不属于上游失败。
const CostOverflowClass = "cost_overflow"

// NewRow 构造计量行：计价经 Cost 逐段向上取整，失败/断连请求也计量，
// 成本按实际 token 计并如实记 error_class。
func NewRow(p RowParams) UsageRow {
	var cost int64
	currency := "CNY"
	if p.Currency != "" {
		currency = p.Currency
	}
	if p.Pricing != nil {
		currency = p.Pricing.Currency
		cost = CostWithCached(p.Usage.PromptTokens, p.Usage.CachedTokens, p.Usage.CompletionTokens,
			p.Pricing.InputCreditsPerM, p.Pricing.CachedInputCreditsPerM, p.Pricing.OutputCreditsPerM)
		if cost < 0 {
			cost = 0
			p.Status, p.ErrorClass, p.Estimated = "error", CostOverflowClass, true
		}
	}
	return UsageRow{
		ID:                  newUsageID(),
		RequestID:           p.RequestID,
		TenantID:            p.TenantID,
		APIKeyID:            p.APIKeyID,
		Provider:            p.Provider,
		ModelAlias:          p.ModelAlias,
		UpstreamModel:       p.UpstreamModel,
		Stream:              p.Stream,
		Status:              p.Status,
		ErrorClass:          p.ErrorClass,
		PromptTokens:        p.Usage.PromptTokens,
		CompletionTokens:    p.Usage.CompletionTokens,
		ReasoningTokens:     p.Usage.ReasoningTokens,
		CachedTokens:        p.Usage.CachedTokens,
		Estimated:           p.Estimated,
		CostCredits:         cost,
		Currency:            currency,
		LatencyFirstTokenMs: p.FirstTokenMs,
		LatencyTotalMs:      p.TotalMs,
		CreatedAt:           timeNowUTC(),
	}
}
