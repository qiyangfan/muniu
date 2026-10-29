package balance

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureVolc 假火山账务端点:捕获鉴权相关头,返回可断言的存根。
func captureVolc(t *testing.T, body string, status int) (*httptest.Server, *volcCaptured) {
	t.Helper()
	cap := &volcCaptured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.auth = r.Header.Get("Authorization")
		cap.xDate = r.Header.Get("X-Date")
		cap.sha = r.Header.Get("X-Content-Sha256")
		cap.query = r.URL.RawQuery
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

type volcCaptured struct {
	auth, xDate, sha, query string
}

func TestVolcBSS_签名链对拍与余额解析(t *testing.T) {
	const ak, sk = "volcAK", "volcSK"
	srv, cap := captureVolc(t, `{"ResponseMetadata":{"RequestId":"x","Action":"QueryBalanceAcct"},
		"Result":{"BalanceList":[{"Balance":"42.80","Currency":"CNY","CashBalance":"42.80"}]}}`, 200)
	q := &volcBSSQuerier{accessKey: ak, secretKey: sk, endpoint: srv.URL}
	b, err := q.Query(context.Background(), "")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if b.Credits != 42_800_000_000 || b.Currency != "CNY" {
		t.Fatalf("volc 余额解析失真: %+v", b)
	}

	// 独立重算:从捕获的 x-date/sha/query 重建 canonical request 与派生密钥链,
	// 与实现侧互为对拍;任何一方退化都会失配。
	if !strings.HasPrefix(cap.xDate, "20") || len(cap.xDate) != 16 || !strings.HasSuffix(cap.xDate, "Z") {
		t.Fatalf("X-Date 形态失真: %q", cap.xDate)
	}
	if cap.sha != hexSha256("") {
		t.Fatalf("payload 哈希应为空体摘要: %q", cap.sha)
	}
	canonicalQuery := "Action=QueryBalanceAcct&Version=2022-01-01"
	if cap.query != canonicalQuery {
		t.Fatalf("query 形态失真: %q", cap.query)
	}
	canonicalHeaders := "content-type:application/json\nhost:" +
		strings.TrimPrefix(srv.URL, "http://") + "\nx-content-sha256:" + cap.sha + "\nx-date:" + cap.xDate + "\n"
	signedHeaders := "content-type;host;x-content-sha256;x-date"
	canonicalRequest := strings.Join([]string{"GET", "/", canonicalQuery, canonicalHeaders, signedHeaders, cap.sha}, "\n")
	scope := cap.xDate[:8] + "/" + volcRegion + "/" + volcService + "/request"
	stringToSign := strings.Join([]string{"HMAC-SHA256", cap.xDate, scope, hexSha256(canonicalRequest)}, "\n")
	key := hmacSHA256([]byte(sk), cap.xDate[:8])
	key = hmacSHA256(key, volcRegion)
	key = hmacSHA256(key, volcService)
	key = hmacSHA256(key, "request")
	wantSig := hex.EncodeToString(hmacSHA256(key, stringToSign))
	if !strings.Contains(cap.auth, "HMAC-SHA256 Credential="+ak+"/"+scope) ||
		!strings.Contains(cap.auth, "SignedHeaders="+signedHeaders) ||
		!strings.Contains(cap.auth, "Signature="+wantSig) {
		t.Fatalf("V4 签名对拍失配:\n got  %s\n want %s", cap.auth, wantSig)
	}
}

func TestVolcBSS_容错提取与错误归类(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		class Class // "" = 成功路径
	}{
		{"Result直挂余额", `{"Result":{"TotalBalance":99.5,"CurrencyCode":"CNY"}}`, ""},
		{"列表形态", `{"Result":{"BalanceList":[{"Balance":"1.00","Currency":"CNY"}]}}`, ""},
		// 2026-10-30 真机实证的官方形态:Result 直挂字符串 AvailableBalance + Currency(无 BalanceList 包装)
		{"真机实证形态", `{"Result":{"AccountID":1,"ArrearsBalance":"0","AvailableBalance":"0","CashBalance":"0","CreditLimit":"0","Currency":"CNY","FreezeAmount":"0"}}`, ""},
		{"错误凭证类", `{"ResponseMetadata":{"Error":{"Code":"InvalidCredential","Message":"x"}}}`, ClassCredential},
		{"错误上游类", `{"ResponseMetadata":{"Error":{"Code":"InternalError"}}}`, ClassUpstream},
		{"无余额字段", `{"Result":{"Foo":"bar"}}`, ClassParse},
		{"缺币种", `{"Result":{"Balance":"1.00"}}`, ClassParse},
	}
	for _, c := range cases {
		srv, _ := captureVolc(t, c.body, 200)
		q := &volcBSSQuerier{accessKey: "a", secretKey: "s", endpoint: srv.URL}
		b, err := q.Query(context.Background(), "")
		if c.class == "" {
			if err != nil {
				t.Fatalf("%s 应成功: %v", c.name, err)
			}
			if b.Currency == "" {
				t.Fatalf("%s 币种不得为空", c.name)
			}
			continue
		}
		var qe *QueryError
		if !errors.As(err, &qe) || qe.Class != c.class {
			t.Fatalf("%s 应归 %s,实际 %v", c.name, c.class, err)
		}
	}
}

// TestVolcBalanceOf_JSON形态 json.Number 与字符串两种余额形态都能提取。
func TestVolcBalanceOf_JSON形态(t *testing.T) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"Balance":12.5,"Currency":"CNY"}`), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	credits, currency, ok := volcBalanceOf(raw)
	if !ok || credits != 12_500_000_000 || currency != "CNY" {
		t.Fatalf("数值形态提取失真: %d %q %v", credits, currency, ok)
	}
}
