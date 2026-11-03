package balance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/solvela/muniu/internal/metering"
)

// 上游余额响应体读取上限:余额端点响应极小,防御性截断防呆上游。
const respLimit = 1 << 20

// balanceHTTP 共享客户端;超时不在此设置,由评估循环统一经 ctx 注入,保证可测可关停。
var balanceHTTP = &http.Client{}

// Class 查询失败归类:决定 last_error 载荷与 balance_source_error 告警文案的区分度。
type Class string

// 失败归类常量。
const (
	ClassTimeout    Class = "timeout"    // ctx 截止(循环统一归此类)
	ClassNetwork    Class = "network"    // 拨号/连接层失败
	ClassHTTP       Class = "http"       // 非 2xx(非凭证类状态码)
	ClassParse      Class = "parse"      // 2xx 但响应不合契约
	ClassCredential Class = "credential" // 401/403 或云签名/权限被拒
	ClassUpstream   Class = "upstream"   // 上游业务错误码(code != 0 等)
)

// QueryError 带归类的查询失败;评估循环经 errors.As 取 Class 写 last_error。
// 网络层包装用 qerrWrap 保留错误链(%w),使 ctx 超时可被 errors.Is 穿透归类。
type QueryError struct {
	Class Class
	Msg   string
	err   error
}

func (e *QueryError) Error() string { return e.Msg }

func (e *QueryError) Unwrap() error { return e.err }

func qerr(class Class, format string, args ...any) *QueryError {
	return &QueryError{Class: class, Msg: fmt.Sprintf(format, args...)}
}

func qerrWrap(class Class, err error, msg string) *QueryError {
	return &QueryError{Class: class, Msg: msg + ": " + err.Error(), err: err}
}

// getJSON Bearer GET 上游并解码;json.Number 保留原始十进制形态,
// 数值一律不经 float64(金额整数纪律在出站侧同样生效)。cl nil = 共享 balanceHTTP。
func getJSON(ctx context.Context, cl *http.Client, endpoint, key string, out any) error {
	if cl == nil {
		cl = balanceHTTP
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return qerr(ClassParse, "balance: 构造请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return qerrWrap(ClassNetwork, err, "balance: 请求上游失败")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return qerr(ClassCredential, "balance: 上游拒绝凭证 HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return qerr(ClassHTTP, "balance: 上游 HTTP %d", resp.StatusCode)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, respLimit))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return qerr(ClassParse, "balance: 响应解析失败: %v", err)
	}
	return nil
}

// upstreamCredits 上游数值转 credit(余额币种 1e-9 刻度)。与配置侧 ParsePrice 的
// 严格纪律不同:上游数值精度不受本仓约束,超 9 位小数截断(误差 ≤1e-9 币种单位,
// 远小于告警档位粒度);允许负值——欠费是真实信号,钳 0 反而丢「已透支」语义。
func upstreamCredits(num json.Number) (int64, error) {
	s := num.String()
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart, frac, hasDot := strings.Cut(s, ".")
	if intPart == "" {
		return 0, qerr(ClassParse, "balance: 上游数值缺整数部分 %q", num.String())
	}
	if !isAllDigits(intPart) || (hasDot && !isAllDigits(frac)) {
		return 0, qerr(ClassParse, "balance: 上游数值非十进制形态 %q", num.String())
	}
	if len(frac) > 9 {
		frac = frac[:9]
	}
	human := intPart
	if frac != "" {
		human += "." + frac
	}
	credits, err := metering.ParsePrice(human)
	if err != nil {
		return 0, qerr(ClassParse, "balance: 上游数值越界 %q", num.String())
	}
	if neg {
		credits = -credits
	}
	return credits, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ---- deepseek:GET /user/balance → balance_infos[].total_balance + currency ----

type deepseekResp struct {
	BalanceInfos []struct {
		Currency     string      `json:"currency"`
		TotalBalance json.Number `json:"total_balance"`
	} `json:"balance_infos"`
}

type bearerQuerier struct {
	kind     string
	endpoint string
	client   *http.Client // 渠道出站代理 client;nil = 共享 balanceHTTP
}

func (q *bearerQuerier) Query(ctx context.Context, key string) (Balance, error) {
	switch q.kind {
	case SourceDeepseek:
		return q.queryDeepseek(ctx, key)
	case SourceMoonshot:
		return q.queryMoonshot(ctx, key)
	case SourceOpenrouter:
		return q.queryOpenrouter(ctx, key)
	default:
		return Balance{}, qerr(ClassParse, "balance: bearerQuerier 不消费 kind %q", q.kind)
	}
}

func (q *bearerQuerier) queryDeepseek(ctx context.Context, key string) (Balance, error) {
	var resp deepseekResp
	if err := getJSON(ctx, q.client, q.endpoint, key, &resp); err != nil {
		return Balance{}, err
	}
	if len(resp.BalanceInfos) == 0 || resp.BalanceInfos[0].TotalBalance.String() == "" {
		return Balance{}, qerr(ClassParse, "balance: deepseek 响应缺 balance_infos")
	}
	first := resp.BalanceInfos[0]
	if first.Currency == "" {
		return Balance{}, qerr(ClassParse, "balance: deepseek 响应缺 currency")
	}
	credits, err := upstreamCredits(first.TotalBalance)
	if err != nil {
		return Balance{}, err
	}
	return Balance{Credits: credits, Currency: first.Currency}, nil
}

// ---- moonshot:GET /v1/users/me/balance → data.available_balance(元,含代金券) ----

type moonshotResp struct {
	Code   int64 `json:"code"`
	Status bool  `json:"status"`
	Data   struct {
		AvailableBalance json.Number `json:"available_balance"`
	} `json:"data"`
}

func (q *bearerQuerier) queryMoonshot(ctx context.Context, key string) (Balance, error) {
	var resp moonshotResp
	if err := getJSON(ctx, q.client, q.endpoint, key, &resp); err != nil {
		return Balance{}, err
	}
	if resp.Code != 0 || !resp.Status {
		return Balance{}, qerr(ClassUpstream, "balance: moonshot 业务错误 code=%d", resp.Code)
	}
	if resp.Data.AvailableBalance.String() == "" || resp.Data.AvailableBalance.String() == "null" {
		return Balance{}, qerr(ClassParse, "balance: moonshot 响应缺 available_balance")
	}
	credits, err := upstreamCredits(resp.Data.AvailableBalance)
	if err != nil {
		return Balance{}, err
	}
	return Balance{Credits: credits, Currency: "CNY"}, nil
}

// ---- openrouter:GET /api/v1/key → data.limit − data.usage;limit=null 不限额 ----

type openrouterResp struct {
	Data struct {
		Usage json.Number `json:"usage"`
		Limit json.Number `json:"limit"` // JSON null = 不限额键
	} `json:"data"`
}

func (q *bearerQuerier) queryOpenrouter(ctx context.Context, key string) (Balance, error) {
	var resp openrouterResp
	if err := getJSON(ctx, q.client, q.endpoint, key, &resp); err != nil {
		return Balance{}, err
	}
	if resp.Data.Usage.String() == "" || resp.Data.Usage.String() == "null" {
		return Balance{}, qerr(ClassParse, "balance: openrouter 响应缺 usage")
	}
	// Unavailable 与查询失败相区分:正常落库(NULL 余额)且不参与余额类告警。
	if resp.Data.Limit.String() == "" || resp.Data.Limit.String() == "null" {
		return Balance{Currency: "USD", Unavailable: true}, nil
	}
	limit, err := upstreamCredits(resp.Data.Limit)
	if err != nil {
		return Balance{}, err
	}
	usage, err := upstreamCredits(resp.Data.Usage)
	if err != nil {
		return Balance{}, err
	}
	if usage < 0 || limit < 0 {
		return Balance{}, qerr(ClassParse, "balance: openrouter usage/limit 出现负值")
	}
	return Balance{Credits: limit - usage, Currency: "USD"}, nil
}

// ---- billing-compat:one-api/new-api 家族 /v1/dashboard/billing/{subscription,usage} ----

type billingCompatQuerier struct {
	subscription string
	usage        string
	currency     string // 余额币种：该字段族随中转实现与站点显示配置而变，不可自描述，由配置显式声明
	client       *http.Client
}

type billingSubResp struct {
	HardLimitUSD json.Number `json:"hard_limit_usd"`
}

type billingUsageResp struct {
	TotalUsage json.Number `json:"total_usage"` // OpenAI legacy 口径:美元分
}

// billingMaxHardCredits 量级护栏:hard_limit_usd 折算 credit 超过 1e7 USD 即判定为
// 「quota 原始值」形态(one-api 关闭货币显示时金额字段实为配额数),不是货币语义,整体判失败。
const billingMaxHardCredits = int64(10_000_000) * 1_000_000_000

func (q *billingCompatQuerier) Query(ctx context.Context, key string) (Balance, error) {
	var sub billingSubResp
	if err := getJSON(ctx, q.client, q.subscription, key, &sub); err != nil {
		return Balance{}, err
	}
	if sub.HardLimitUSD.String() == "" || sub.HardLimitUSD.String() == "null" {
		return Balance{}, qerr(ClassParse, "balance: billing-compat 响应缺 hard_limit_usd")
	}
	hard, err := upstreamCredits(sub.HardLimitUSD)
	if err != nil {
		return Balance{}, err
	}
	if hard > billingMaxHardCredits {
		return Balance{}, qerr(ClassParse,
			"balance: billing-compat hard_limit_usd 超出货币量级(疑为 quota 原始值形态)")
	}
	var use billingUsageResp
	if err := getJSON(ctx, q.client, q.usage, key, &use); err != nil {
		return Balance{}, err
	}
	if use.TotalUsage.String() == "" || use.TotalUsage.String() == "null" {
		return Balance{}, qerr(ClassParse, "balance: billing-compat 响应缺 total_usage")
	}
	usageRaw, err := upstreamCredits(use.TotalUsage)
	if err != nil {
		return Balance{}, err
	}
	if usageRaw < 0 {
		return Balance{}, qerr(ClassParse, "balance: billing-compat total_usage 出现负值")
	}
	// total_usage 数值单位按声明币种的百分之一(OpenAI legacy 口径的分):1 分 = 1e-2 币种单位
	// = 1e7 credit,即 usageRaw(数值×1e9)除以 100;除法精确,1e9 刻度天然整除。
	return Balance{Credits: hard - usageRaw/100, Currency: q.currency}, nil
}
