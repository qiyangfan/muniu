package balance

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// aliyunBSS 阿里云账务 OpenAPI 端点与参数常量(RPC 形态,POST form + HMAC-SHA1 签名)。
// 权限要求:AccessKey 属 RAM 子账号并只授账务只读(如 AliyunBSSReadOnlyAccess)。
// 端点为 business.aliyuncs.com:旧 bssopenapi.aliyuncs.com 2026-10-30 实测已 NXDOMAIN 退役
// (阿里公共 DNS 223.5.5.5 同样不可解析),business 域名签名校验通过。
const (
	aliyunBSSEndpoint = "https://business.aliyuncs.com/"
	aliyunBSSAction   = "QueryAccountBalance"
	aliyunBSSVersion  = "2017-12-14"
)

type aliyunBSSQuerier struct {
	accessKey string
	secretKey string
	endpoint  string       // 空 = 官方端点;测试注入 stub
	client    *http.Client // 渠道出站代理 client;nil = 共享 balanceHTTP
}

func (q *aliyunBSSQuerier) addr() string {
	if q.endpoint != "" {
		return q.endpoint
	}
	return aliyunBSSEndpoint
}

type aliyunBSSResp struct {
	Code    string `json:"Code"` // 非空即错误;成功响应无 Code 字段
	Message string `json:"Message"`
	Data    struct {
		AvailableAmount any    `json:"AvailableAmount"` // 官方为字符串十进制,兼容数值形态
		Currency        string `json:"Currency"`
	} `json:"Data"`
}

// Query 查询阿里云账户可用余额(CNY)。key 参数忽略:账户级源,凭证构造期已固定。
func (q *aliyunBSSQuerier) Query(ctx context.Context, _ string) (Balance, error) {
	params := map[string]string{
		"Action":           aliyunBSSAction,
		"AccessKeyId":      q.accessKey,
		"Format":           "JSON",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   nonceHex(),
		"SignatureVersion": "1.0",
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          aliyunBSSVersion,
	}
	// StringToSign = POST&%2F&encode(canonicalQuery):canonical 先按签名规则编码,
	// 再整体二次编码,双重编码是阿里云 RPC 签名的固定形态。
	canonical := aliyunCanonicalQuery(params)
	mac := hmac.New(sha1.New, []byte(q.secretKey+"&"))
	mac.Write([]byte("POST&%2F&" + aliyunEncode(canonical)))
	params["Signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.addr(),
		strings.NewReader(form.Encode()))
	if err != nil {
		return Balance{}, qerr(ClassParse, "balance: aliyun 构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return aliyunExecute(ctx, req, q.client)
}

func aliyunExecute(ctx context.Context, req *http.Request, cl *http.Client) (Balance, error) {
	if cl == nil {
		cl = balanceHTTP
	}
	resp, err := cl.Do(req)
	if err != nil {
		return Balance{}, qerrWrap(ClassNetwork, err, "balance: aliyun 请求失败")
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, rerr := io.ReadAll(io.LimitReader(resp.Body, respLimit))
	if rerr != nil {
		return Balance{}, qerr(ClassParse, "balance: aliyun 响应读取失败: %v", rerr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 阿里云 RPC 惯例:凭证/签名类错误以非 2xx + JSON body(Code 字段)返回,
		// 此处必须读 body 归类,否则凭证问题退化为通用 HTTP 错误
		var errBody aliyunBSSResp
		if jerr := json.Unmarshal(bodyBytes, &errBody); jerr == nil && errBody.Code != "" {
			return Balance{}, qerr(aliyunClassOf(errBody.Code), "balance: aliyun 错误 %s", errBody.Code)
		}
		return Balance{}, qerr(ClassHTTP, "balance: aliyun HTTP %d", resp.StatusCode)
	}
	var body aliyunBSSResp
	// UseNumber 解码:余额数值形态保住十进制原样,不落 float64(金额整数纪律)
	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return Balance{}, qerr(ClassParse, "balance: aliyun 响应解析失败: %v", err)
	}
	if body.Code != "" && body.Code != "200" {
		return Balance{}, qerr(aliyunClassOf(body.Code),
			"balance: aliyun 错误 %s", body.Code)
	}
	// business 域名的现行成功惯例带 "Code":"200"+"Success":true(2026-10-30 真机实证),
	// 与旧惯例「成功无 Code 字段」并存;错误判定只认非 200 的非空 Code。
	amount, err := balanceAmountOf(body.Data.AvailableAmount)
	if err != nil {
		return Balance{}, err
	}
	if body.Data.Currency == "" {
		return Balance{}, qerr(ClassParse, "balance: aliyun 响应缺 Currency")
	}
	return Balance{Credits: amount, Currency: body.Data.Currency}, nil
}

// aliyunClassOf 错误码归类:凭证/权限类错误单独成类,与网络故障在上层区分处置文案。
func aliyunClassOf(code string) Class {
	for _, marker := range []string{"Signature", "AccessKeyId", "Forbidden", "Permission", "Auth"} {
		if strings.Contains(code, marker) {
			return ClassCredential
		}
	}
	return ClassUpstream
}

// aliyunCanonicalQuery 参数按 key 字典序排列后 k=encode(v) 以 & 连接(签名的基准串)。
func aliyunCanonicalQuery(params map[string]string) string {
	keys := sortKeys(params)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+aliyunEncode(params[k]))
	}
	return strings.Join(parts, "&")
}

// aliyunEncode RFC3986 percent-encoding,阿里云签名规范:空格为 %20(非 +),
// 波浪号豁免,其余非预留字符全量 %XX 大写。
func aliyunEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// balanceAmountOf 兼容字符串/数值两种形态的余额字段。
func balanceAmountOf(v any) (int64, error) {
	switch n := v.(type) {
	case string:
		if n == "" {
			return 0, qerr(ClassParse, "balance: 余额字段为空")
		}
		return upstreamCredits(json.Number(n))
	case json.Number:
		return upstreamCredits(n)
	default:
		return 0, qerr(ClassParse, "balance: 余额字段缺失")
	}
}

// nonceHex 每请求唯一的签名 nonce(crypto/rand,16 字节 hex),防重放校验所需。
func nonceHex() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
