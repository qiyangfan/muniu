// Package balance 承载渠道余额观测:多源余额查询(Bearer 直查/云账务签名)、
// 有效余额合成、燃速外推与阈值档位告警状态机。评估循环异步于数据面,
// 查询失败只留痕不干扰转发;未声明余额源的渠道零行为(零请求/零落库/零告警)。
package balance

import (
	"context"
	"fmt"
	"net/http"
	"sort"
)

// kind 值域单一事实点:config 校验、example yaml 与文档共同引用本清单。
// siliconflow 曾在列:官方余额 API 已于 2026-08-14 退役(HTTP 410)且替代 API 未发布,
// 2026-10-30 owner 裁决整档移除;官方「账户层面替代 API」公告出现后按 Bearer 族配方重新接入。
const (
	SourceNone          = "none"
	SourceManual        = "manual"
	SourceDeepseek      = "deepseek"
	SourceMoonshot      = "moonshot"
	SourceOpenrouter    = "openrouter"
	SourceBillingCompat = "billing-compat"
	SourceAliyunBSS     = "aliyun-bss"
	SourceVolcBSS       = "volc-bss"
)

// Types 全部合法余额源 kind;manual/none 无查询器,由评估服务另行处理。
func Types() []string {
	return []string{
		SourceNone, SourceManual, SourceDeepseek, SourceMoonshot,
		SourceOpenrouter, SourceBillingCompat, SourceAliyunBSS, SourceVolcBSS,
	}
}

// bearerKinds 用渠道 key 池逐 key Bearer 直查的 kind 集合;其余非 none/manual 的
// kind 均为账户级(云账务 AK/SK),每渠道只查一次。
var bearerKinds = map[string]bool{
	SourceDeepseek: true, SourceMoonshot: true,
	SourceOpenrouter: true, SourceBillingCompat: true,
}

// IsBearer 报告 kind 是否为 Bearer 直查源(装配层据此决定逐 key 还是单次查询)。
func IsBearer(kind string) bool { return bearerKinds[kind] }

// Balance 查询结果。Credits 以余额币种 1e-9 刻度整数存储(与 cost_credits 同纪律,
// 全程零 float);Unavailable 表示上游明确「余额不可知」(openrouter 不限额键),
// 与查询失败相区分:前者正常落库且不参与告警,后者进入失败容忍路径。
type Balance struct {
	Credits     int64
	Currency    string
	Unavailable bool
}

// Querier 单渠道余额源查询器。Bearer 源每把 key 各构造一次语义(key 参数传入);
// 云账务源忽略 key(账户级,构造期已持有 AK/SK)。
type Querier interface {
	Query(ctx context.Context, key string) (Balance, error)
}

// SourceConfig 单渠道余额源构造参数。AccessKey/SecretKey 由装配层在启动时经环境变量
// 解析为明文传入,仅存活于内存;绝不写日志、落库或进入告警载荷。
type SourceConfig struct {
	BaseURL   string // 渠道 base_url;Bearer 源取其 origin 拼余额端点
	AccessKey string // 云账务 AccessKey ID
	SecretKey string // 云账务 Secret Key
	Currency  string // billing-compat 显式币种声明(其余源忽略);空=缺省 USD
}

// ErrUnknownSource kind 不在值域时的哨兵错误(装配期 fail-fast,config 校验为第一道防线)。
type ErrUnknownSource struct{ Kind string }

func (e ErrUnknownSource) Error() string {
	return fmt.Sprintf("balance: 未知余额源 kind %q", e.Kind)
}

// For 按 kind 构造查询器;none/manual 返回 nil(无查询行为,由服务层处理)。
func For(kind string, cfg SourceConfig) (Querier, error) {
	return ForWithClient(kind, cfg, nil)
}

// ForWithClient 同 For,但绑定渠道出站 client(渠道声明代理时由装配层注入);nil = 共享
// balanceHTTP(现状语义)。渠道代理覆盖余额查询面,与推理/探针同纪律。
func ForWithClient(kind string, cfg SourceConfig, cl *http.Client) (Querier, error) {
	switch kind {
	case SourceNone, SourceManual:
		return nil, nil
	case SourceDeepseek:
		return &bearerQuerier{kind: kind, endpoint: origin(cfg.BaseURL) + "/user/balance", client: cl}, nil
	case SourceMoonshot:
		return &bearerQuerier{kind: kind, endpoint: origin(cfg.BaseURL) + "/v1/users/me/balance", client: cl}, nil
	case SourceOpenrouter:
		return &bearerQuerier{kind: kind, endpoint: origin(cfg.BaseURL) + "/api/v1/key", client: cl}, nil
	case SourceBillingCompat:
		currency := cfg.Currency
		if currency == "" {
			currency = "USD" // OpenAI 官方端点的字段语义;中转站点可显式声明覆盖
		}
		return &billingCompatQuerier{
			subscription: origin(cfg.BaseURL) + "/v1/dashboard/billing/subscription",
			usage:        origin(cfg.BaseURL) + "/v1/dashboard/billing/usage",
			currency:     currency,
			client:       cl,
		}, nil
	case SourceAliyunBSS:
		return &aliyunBSSQuerier{accessKey: cfg.AccessKey, secretKey: cfg.SecretKey, client: cl}, nil
	case SourceVolcBSS:
		return &volcBSSQuerier{accessKey: cfg.AccessKey, secretKey: cfg.SecretKey, client: cl}, nil
	default:
		return nil, ErrUnknownSource{Kind: kind}
	}
}

// origin 从 base_url 提取 scheme://host:port。余额端点与推理端点的路径习惯不一致
// (deepseek /user/balance 不带 /v1、moonshot 带 /v1),按各家官方文档取固定路径,
// 只继承 base_url 的协议与主机,拼接版本段会在部分家产出 //v1/v1 形态。
func origin(baseURL string) string {
	// base_url 已过 config 校验(合法 http(s) URL),此处只做容错截取。
	for i := 0; i < len(baseURL); i++ {
		if baseURL[i] == '/' && i > 0 && baseURL[i-1] == '/' { // 跳过 "://" 的第二个斜杠
			rest := baseURL[i+1:]
			if j := indexSlash(rest); j >= 0 {
				return baseURL[:i+1+j]
			}
			return baseURL
		}
	}
	return baseURL
}

func indexSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// sortKeys map 键稳定排序,签名参数序列化共用。
func sortKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
