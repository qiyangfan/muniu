package balance

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// volcBSS 火山引擎账务 OpenAPI(Signature V4,HMAC-SHA256 派生密钥链)。
// 权限要求:IAM 子账号最小授权账务只读;service/region 为账务 API 固定作用域。
const (
	volcBSSEndpoint = "https://open.volcengineapi.com/"
	volcBSSAction   = "QueryBalanceAcct"
	volcBSSVersion  = "2022-01-01"
	volcService     = "billing"
	volcRegion      = "cn-north-1"
)

type volcBSSQuerier struct {
	accessKey string
	secretKey string
	endpoint  string       // 空 = 官方端点;测试注入 stub
	client    *http.Client // 渠道出站代理 client;nil = 共享 balanceHTTP
}

func (q *volcBSSQuerier) addr() string {
	if q.endpoint != "" {
		return q.endpoint
	}
	return volcBSSEndpoint
}

type volcBSSResp struct {
	ResponseMetadata *struct {
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
	Result map[string]json.RawMessage `json:"Result"`
}

// volcBalanceKeys 候选余额字段名:响应精确形态未真机实证(见 change design Open Questions),
// 解析器按字段名容错,找不到即判解析失败并如实留痕,不猜数值。
var volcBalanceKeys = []string{"TotalBalance", "Balance", "AvailableBalance"}

// Query 查询火山引擎账户余额(CNY)。key 参数忽略:账户级源。
func (q *volcBSSQuerier) Query(ctx context.Context, _ string) (Balance, error) {
	req, err := q.signedRequest(ctx)
	if err != nil {
		return Balance{}, err
	}
	return volcExecute(req, q.client)
}

// signedRequest 构造 V4 签名 GET 请求。
func (q *volcBSSQuerier) signedRequest(ctx context.Context) (*http.Request, error) {
	now := time.Now().UTC()
	xDate := now.Format("20060102T150405Z")
	query := url.Values{"Action": {volcBSSAction}, "Version": {volcBSSVersion}}
	canonicalQuery := volcCanonicalQuery(query)
	payloadHash := hexSha256("")
	host := volcHostOf(q.addr())
	signedHeaders := "content-type;host;x-content-sha256;x-date"
	canonicalHeaders := "content-type:application/json\n" +
		"host:" + host + "\n" +
		"x-content-sha256:" + payloadHash + "\n" +
		"x-date:" + xDate + "\n"
	canonicalRequest := strings.Join([]string{
		http.MethodGet, "/", canonicalQuery, canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	scope := volcScope(xDate)
	stringToSign := strings.Join([]string{
		"HMAC-SHA256", xDate, scope, hexSha256(canonicalRequest),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(volcSigningKey(q.secretKey, xDate[:8]), stringToSign))

	endpoint := q.addr() + "?" + canonicalQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, qerr(ClassParse, "balance: volc 构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Date", xDate)
	req.Header.Set("X-Content-Sha256", payloadHash)
	req.Header.Set("Authorization", fmt.Sprintf(
		"HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		q.accessKey, scope, signedHeaders, signature))
	return req, nil
}

// volcScope V4 作用域:日期/region/service/终止符。
func volcScope(xDate string) string {
	return xDate[:8] + "/" + volcRegion + "/" + volcService + "/request"
}

// volcCanonicalQuery V4 canonical query:string 键字典序,RFC3986 编码(空格 %20),
// 不用 QueryEscape 的 + 空格形态。
func volcCanonicalQuery(query url.Values) string {
	keys := sortKeys2(query)
	encoded := make([]string, 0, len(keys))
	for _, k := range keys {
		encoded = append(encoded, aliyunEncode(k)+"="+aliyunEncode(query.Get(k)))
	}
	return strings.Join(encoded, "&")
}

// volcHostOf 从端点 URL 取出签名用的 host(去 scheme 与 path)。
func volcHostOf(base string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	return host
}

// volcSigningKey V4 派生密钥链:日期 → region → service → "request"。
// 终止符为 "request":按官方 volc-sdk-golang base/sign.go signingKeyV4 对拍修正,
// 旧实现的 "volcengine" 与测试链同源自洽、单测测不出,2026-10-30 真机 SignatureDoesNotMatch 实证后修正。
func volcSigningKey(secret, date string) []byte {
	key := hmacSHA256([]byte(secret), date)
	key = hmacSHA256(key, volcRegion)
	key = hmacSHA256(key, volcService)
	return hmacSHA256(key, "request")
}

// volcExecute 发请求并解析响应。
func volcExecute(req *http.Request, cl *http.Client) (Balance, error) {
	if cl == nil {
		cl = balanceHTTP
	}
	resp, err := cl.Do(req)
	if err != nil {
		return Balance{}, qerrWrap(ClassNetwork, err, "balance: volc 请求失败")
	}
	defer func() { _ = resp.Body.Close() }()
	if err := volcStatusError(resp.StatusCode); err != nil {
		return Balance{}, err
	}
	var body volcBSSResp
	dec := json.NewDecoder(io.LimitReader(resp.Body, respLimit))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return Balance{}, qerr(ClassParse, "balance: volc 响应解析失败: %v", err)
	}
	if md := body.ResponseMetadata; md != nil && md.Error != nil && md.Error.Code != "" {
		return Balance{}, qerr(volcClassOf(md.Error.Code), "balance: volc 错误 %s", md.Error.Code)
	}
	credits, currency, ok := volcBalanceOf(body.Result)
	if !ok {
		return Balance{}, qerr(ClassParse, "balance: volc 响应未找到余额字段(待真机实证响应形态)")
	}
	if currency == "" {
		return Balance{}, qerr(ClassParse, "balance: volc 响应缺币种字段")
	}
	return Balance{Credits: credits, Currency: currency}, nil
}

// volcStatusError 非 2xx 归类:401/403 归凭证类(便于与网络故障区分处置)。
func volcStatusError(status int) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return qerr(ClassCredential, "balance: volc 拒绝凭证 HTTP %d", status)
	}
	if status < 200 || status >= 300 {
		return qerr(ClassHTTP, "balance: volc HTTP %d", status)
	}
	return nil
}

// volcBalanceOf 容错提取余额:优先 Result.BalanceList[0],退回 Result 本身;
// 余额/币种按候选字段名查找。
func volcBalanceOf(result map[string]json.RawMessage) (int64, string, bool) {
	source := volcBalanceSource(result)
	if source == nil {
		return 0, "", false
	}
	credits, ok := volcCreditsOf(source)
	if !ok {
		return 0, "", false
	}
	return credits, volcCurrencyOf(source), true
}

// volcBalanceSource 选余额所在对象:BalanceList 首元素优先,缺失/空表退回 Result 本身。
func volcBalanceSource(result map[string]json.RawMessage) map[string]json.RawMessage {
	if result == nil {
		return nil
	}
	raw, ok := result["BalanceList"]
	if !ok {
		return result
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 {
		return list[0]
	}
	return result
}

// volcCreditsOf 按候选字段名找余额,多形态容错(见 volcBalanceKeys)。
func volcCreditsOf(source map[string]json.RawMessage) (int64, bool) {
	for _, key := range volcBalanceKeys {
		raw, ok := source[key]
		if !ok {
			continue
		}
		if n, ok := volcRawCredits(raw); ok {
			return n, true
		}
	}
	return 0, false
}

// volcRawCredits 单个字段值 → credit;数值形态保住十进制原样,不落 float64(金额整数纪律)。
func volcRawCredits(raw json.RawMessage) (int64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false
	}
	n, err := balanceAmountOf(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

// volcCurrencyOf 按候选字段名找币种;缺失或空串返回空。
func volcCurrencyOf(source map[string]json.RawMessage) string {
	for _, key := range []string{"Currency", "CurrencyCode"} {
		raw, ok := source[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}

// volcClassOf 错误码归类:凭证/权限类单独成类。
func volcClassOf(code string) Class {
	for _, marker := range []string{"Signature", "AccessKey", "Auth", "Permission", "Credential", "Forbidden"} {
		if strings.Contains(code, marker) {
			return ClassCredential
		}
	}
	return ClassUpstream
}

func hexSha256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

// sortKeys2 url.Values 键稳定排序(V4 canonical query 要求字典序)。
func sortKeys2(v url.Values) []string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ { // 插入排序:键数恒为 2,免引 sort 缩小依赖面
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
