package balance

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// aliyunSignForTest 测试侧独立实现的最小签名器,与实现代码互为对拍:
// 实现侧参数拼装或编码退化时,期望签名与收到的不一致。
func aliyunSignForTest(params url.Values, secretKey string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+aliyunEncode(params.Get(k)))
	}
	canonical := strings.Join(parts, "&")
	mac := hmac.New(sha1.New, []byte(secretKey+"&"))
	mac.Write([]byte("POST&%2F&" + aliyunEncode(canonical)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestAliyunBSS_签名对拍与余额解析(t *testing.T) {
	const ak, sk = "testAKID", "testSecret"
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("form 解析: %v", err)
		}
		got = r.PostForm
		// 2026-10-30 真机实证的 business 域名现行成功惯例:Code:"200"+Success:true(非旧惯例的无 Code)
		_, _ = w.Write([]byte(`{"Message":"success","RequestId":"x","Data":{"AvailableCashAmount":"39.44","MybankCreditAmount":"0.00","Currency":"CNY","AvailableAmount":"123.45","CreditAmount":"0.00","QuotaLimit":"0.00"},"Code":"200","Success":true}`))
	}))
	t.Cleanup(srv.Close)

	q := &aliyunBSSQuerier{accessKey: ak, secretKey: sk, endpoint: srv.URL}
	b, err := q.Query(context.Background(), "")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if b.Credits != 123_450_000_000 || b.Currency != "CNY" {
		t.Fatalf("aliyun 余额解析失真: %+v", b)
	}
	if got.Get("AccessKeyId") != ak || got.Get("Action") != "QueryAccountBalance" ||
		got.Get("Version") != aliyunBSSVersion || got.Get("Format") != "JSON" ||
		got.Get("SignatureMethod") != "HMAC-SHA1" || got.Get("SignatureVersion") != "1.0" {
		t.Fatalf("签名参数缺失: %v", got)
	}
	ts := got.Get("Timestamp")
	if !strings.HasSuffix(ts, "Z") || len(ts) != 20 || strings.Contains(ts, "+") {
		t.Fatalf("Timestamp 须为 UTC ISO8601 秒级: %q", ts)
	}
	if expect := aliyunSignForTest(got, sk); got.Get("Signature") != expect {
		t.Fatalf("签名对拍失配:\n got  %s\n want %s", got.Get("Signature"), expect)
	}

	// nonce 唯一:两次查询不得复用
	first := got.Get("SignatureNonce")
	if _, err := q.Query(context.Background(), ""); err != nil {
		t.Fatalf("二次查询失败: %v", err)
	}
	if got.Get("SignatureNonce") == first {
		t.Fatalf("SignatureNonce 重复")
	}
}

func TestAliyunBSS_错误码归类与缺币种(t *testing.T) {
	cases := []struct {
		body  string
		class Class
	}{
		{`{"Code":"SignatureDoesNotMatch","Message":"bad"}`, ClassCredential},
		{`{"Code":"InvalidAccessKeyId.NotFound"}`, ClassCredential},
		{`{"Code":"Forbidden.RAM"}`, ClassCredential},
		{`{"Code":"InternalError","Message":"oops"}`, ClassUpstream},
		{`{"Data":{"AvailableAmount":"1.00"}}`, ClassParse}, // 缺 Currency
		{"http403带凭证码", ClassCredential},                    // 非 2xx + body Code 的真实云错误形态
	}
	for i, c := range cases {
		status := 200
		bodyText := c.body
		if c.class == ClassCredential && c.body == "http403带凭证码" {
			status, bodyText = 403, `{"Code":"SignatureDoesNotMatch","Message":"x"}`
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(bodyText))
		}))
		q := &aliyunBSSQuerier{accessKey: "a", secretKey: "s", endpoint: srv.URL}
		_, err := q.Query(context.Background(), "")
		srv.Close()
		var qe *QueryError
		if !errors.As(err, &qe) || qe.Class != c.class {
			t.Fatalf("case %d %s 应归 %s,实际 %v", i, c.body, c.class, err)
		}
	}
}

func TestAliyunBSS_数值形态余额兼容(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Data":{"AvailableAmount":-3.20,"Currency":"CNY"}}`))
	}))
	t.Cleanup(srv.Close)
	q := &aliyunBSSQuerier{accessKey: "a", secretKey: "s", endpoint: srv.URL}
	b, err := q.Query(context.Background(), "")
	if err != nil || b.Credits != -3_200_000_000 {
		t.Fatalf("数值形态/欠费负值解析失真: %+v %v", b, err)
	}
}
