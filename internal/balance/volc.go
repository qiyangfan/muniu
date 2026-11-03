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
	now := time.Now().UTC()
	xDate := now.Format("20060102T150405Z")
	payloadHash := hexSha256("")

	query := url.Values{"Action": {volcBSSAction}, "Version": {volcBSSVersion}}
	keys := sortKeys2(query)
	encoded := make([]string, 0, len(keys))
	for _, k := range keys {
		// V4 canonical query 用 RFC3986 编码(空格 %20),不用 QueryEscape 的 + 空格形态
		encoded = append(encoded, aliyunEncode(k)+"="+aliyunEncode(query.Get(k)))
	}
	canonicalQuery := strings.Join(encoded, "&")

	base := q.addr()
	host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	signedHeaders := "content-type;host;x-content-sha256;x-date"
	canonicalHeaders := "content-type:application/json\n" +
		"host:" + host + "\n" +
		"x-content-sha256:" + payloadHash + "\n" +
		"x-date:" + xDate + "\n"

	canonicalRequest := strings.Join([]string{
		http.MethodGet, "/", canonicalQuery, canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	scope := xDate[:8] + "/" + volcRegion + "/" + volcService + "/request"
	stringToSign := strings.Join([]string{
		"HMAC-SHA256", xDate, scope, hexSha256(canonicalRequest),
	}, "\n")

	key := hmacSHA256([]byte(q.secretKey), xDate[:8])
	key = hmacSHA256(key, volcRegion)
	key = hmacSHA256(key, volcService)
	// 终止符为 "request":按官方 volc-sdk-golang base/sign.go signingKeyV4 对拍修正,
	// 旧实现的 "volcengine" 与测试链同源自洽、单测测不出,2026-10-30 真机 SignatureDoesNotMatch 实证后修正。
	key = hmacSHA256(key, "request")
	signature := hex.EncodeToString(hmacSHA256(key, stringToSign))

	endpoint := q.addr() + "?" + canonicalQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Balance{}, qerr(ClassParse, "balance: volc 构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Date", xDate)
	req.Header.Set("X-Content-Sha256", payloadHash)
	req.Header.Set("Authorization", fmt.Sprintf(
		"HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		q.accessKey, scope, signedHeaders, signature))

	cl := q.client
	if cl == nil {
		cl = balanceHTTP
	}
	resp, err := cl.Do(req)
	if err != nil {
		return Balance{}, qerrWrap(ClassNetwork, err, "balance: volc 请求失败")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Balance{}, qerr(ClassCredential, "balance: volc 拒绝凭证 HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Balance{}, qerr(ClassHTTP, "balance: volc HTTP %d", resp.StatusCode)
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

// volcBalanceOf 容错提取余额:优先 Result.BalanceList[0],退回 Result 本身;
// 余额/币种按候选字段名查找。
func volcBalanceOf(result map[string]json.RawMessage) (int64, string, bool) {
	if result == nil {
		return 0, "", false
	}
	source := result
	if raw, ok := result["BalanceList"]; ok {
		var list []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 {
			source = list[0]
		}
	}
	var credits int64
	var found bool
	for _, key := range volcBalanceKeys {
		if raw, ok := source[key]; ok {
			// UseNumber 解码:数值形态保住十进制原样,不落 float64(金额整数纪律)
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			var v any
			if err := dec.Decode(&v); err == nil {
				if n, err := balanceAmountOf(v); err == nil {
					credits, found = n, true
					break
				}
			}
		}
	}
	if !found {
		return 0, "", false
	}
	currency := ""
	for _, key := range []string{"Currency", "CurrencyCode"} {
		if raw, ok := source[key]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil && s != "" {
				currency = s
				break
			}
		}
	}
	return credits, currency, true
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
