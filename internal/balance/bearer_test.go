package balance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFor_注册表值域(t *testing.T) {
	cfg := SourceConfig{BaseURL: "https://api.example.com"}
	for _, kind := range []string{SourceDeepseek, SourceMoonshot, SourceOpenrouter, SourceBillingCompat} {
		q, err := For(kind, cfg)
		if err != nil || q == nil {
			t.Fatalf("kind %s 应构造出查询器: %v", kind, err)
		}
	}
	if !IsBearer(SourceDeepseek) || IsBearer(SourceAliyunBSS) {
		t.Fatalf("IsBearer 分类失真")
	}
	for _, kind := range []string{SourceNone, SourceManual} {
		q, err := For(kind, cfg)
		if err != nil || q != nil {
			t.Fatalf("kind %s 应无查询器: %v %v", kind, q, err)
		}
	}
	for _, kind := range []string{SourceAliyunBSS, SourceVolcBSS} {
		q, err := For(kind, cfg)
		if err != nil || q == nil {
			t.Fatalf("cloud kind %s 应构造出查询器: %v", kind, err)
		}
	}
	if _, err := For("gemini", cfg); !errors.As(err, &ErrUnknownSource{}) {
		t.Fatalf("值域外 kind 应报 ErrUnknownSource,实际 %v", err)
	}
}

func TestOrigin_取协议与主机不含路径(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com":              "https://api.deepseek.com",
		"https://api.deepseek.com/":             "https://api.deepseek.com",
		"https://api.moonshot.cn/v1":            "https://api.moonshot.cn",
		"https://api.moonshot.cn/v1/":           "https://api.moonshot.cn",
		"http://127.0.0.1:8787/v1":              "http://127.0.0.1:8787",
		"https://gateway.example.com/sub/path/": "https://gateway.example.com",
	}
	for in, want := range cases {
		if got := origin(in); got != want {
			t.Fatalf("origin(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// newBalanceStub httptest 假上游:返回 body 与状态码,并回传自身 URL 供构造端点。
func newBalanceStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustBalance(t *testing.T, b Balance, err error) Balance {
	t.Helper()
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	return b
}

func TestDeepseek_多币种取首条与失败路径(t *testing.T) {
	srv := newBalanceStub(t, 200, `{"is_available":true,"balance_infos":[
		{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"},
		{"currency":"USD","total_balance":"1.00"}]}`)
	q, _ := For(SourceDeepseek, SourceConfig{BaseURL: srv.URL})
	b, balErr := q.Query(context.Background(), "sk-test")
	b = mustBalance(t, b, balErr)
	if b.Credits != 110_000_000_000 || b.Currency != "CNY" || b.Unavailable {
		t.Fatalf("deepseek 解析失真: %+v", b)
	}

	// 非 2xx
	srv = newBalanceStub(t, 500, `{}`)
	q, _ = For(SourceDeepseek, SourceConfig{BaseURL: srv.URL})
	_, err := q.Query(context.Background(), "k")
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Class != ClassHTTP {
		t.Fatalf("500 应归 ClassHTTP: %v", err)
	}
	// 缺字段
	srv = newBalanceStub(t, 200, `{"is_available":true,"balance_infos":[]}`)
	q, _ = For(SourceDeepseek, SourceConfig{BaseURL: srv.URL})
	_, err = q.Query(context.Background(), "k")
	if !errors.As(err, &qe) || qe.Class != ClassParse {
		t.Fatalf("缺 balance_infos 应归 ClassParse: %v", err)
	}
	// 401 → 凭证类
	srv = newBalanceStub(t, 401, `{"error":{}}`)
	q, _ = For(SourceDeepseek, SourceConfig{BaseURL: srv.URL})
	_, err = q.Query(context.Background(), "k")
	if !errors.As(err, &qe) || qe.Class != ClassCredential {
		t.Fatalf("401 应归 ClassCredential: %v", err)
	}
}

func TestMoonshot_数值形态解析(t *testing.T) {
	// 49.58894 为 JSON 数值;经 json.Number 全程不触 float64
	srv := newBalanceStub(t, 200, `{"code":0,"data":{"available_balance":49.58894,"voucher_balance":46.58893,"cash_balance":3.00001},"scode":"0x0","status":true}`)
	q, _ := For(SourceMoonshot, SourceConfig{BaseURL: srv.URL})
	b, balErr := q.Query(context.Background(), "k")
	b = mustBalance(t, b, balErr)
	if b.Credits != 49_588_940_000 || b.Currency != "CNY" {
		t.Fatalf("moonshot 解析失真: %+v", b)
	}
	srv = newBalanceStub(t, 200, `{"code":1,"message":"余额异常","status":false}`)
	q, _ = For(SourceMoonshot, SourceConfig{BaseURL: srv.URL})
	_, err := q.Query(context.Background(), "k")
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Class != ClassUpstream {
		t.Fatalf("业务错误应归 ClassUpstream: %v", err)
	}
}

func TestOpenrouter_限额与不限额(t *testing.T) {
	srv := newBalanceStub(t, 200, `{"data":{"label":"k","usage":37.2,"limit":100,"is_free_tier":false}}`)
	q, _ := For(SourceOpenrouter, SourceConfig{BaseURL: srv.URL})
	b, balErr := q.Query(context.Background(), "k")
	b = mustBalance(t, b, balErr)
	if b.Credits != 62_800_000_000 || b.Currency != "USD" || b.Unavailable {
		t.Fatalf("openrouter 求差失真: %+v", b)
	}
	// 不限额键:limit=null → Unavailable,余额不可知
	srv = newBalanceStub(t, 200, `{"data":{"label":"k","usage":12.5,"limit":null}}`)
	q, _ = For(SourceOpenrouter, SourceConfig{BaseURL: srv.URL})
	b, balErr = q.Query(context.Background(), "k")
	b = mustBalance(t, b, balErr)
	if !b.Unavailable || b.Currency != "USD" {
		t.Fatalf("不限额键应 Unavailable: %+v", b)
	}
}

func TestBillingCompat_求差与护栏(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/dashboard/billing/subscription":
			_, _ = w.Write([]byte(`{"object":"billing_subscription","hard_limit_usd":50.0,"system_hard_limit_usd":50.0}`))
		case "/v1/dashboard/billing/usage":
			_, _ = w.Write([]byte(`{"object":"list","total_usage":1250}`))
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	q, _ := For(SourceBillingCompat, SourceConfig{BaseURL: srv.URL})
	b, balErr := q.Query(context.Background(), "k")
	b = mustBalance(t, b, balErr)
	if b.Credits != 37_500_000_000 || b.Currency != "USD" {
		t.Fatalf("billing-compat 求差失真: %+v", b)
	}

	// quota 原始值形态(one-api 关闭货币显示):量级护栏整体判失败
	guard := newBalanceStub(t, 200, `{"hard_limit_usd":500000000,"total_usage":0}`)
	q, _ = For(SourceBillingCompat, SourceConfig{BaseURL: guard.URL})
	_, err := q.Query(context.Background(), "k")
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Class != ClassParse {
		t.Fatalf("量级护栏应 ClassParse: %v", err)
	}
	// 单端点失败整体失败
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/usage") {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"hard_limit_usd":50}`))
	}))
	t.Cleanup(broken.Close)
	q, _ = For(SourceBillingCompat, SourceConfig{BaseURL: broken.URL})
	_, err = q.Query(context.Background(), "k")
	if !errors.As(err, &qe) || qe.Class != ClassHTTP {
		t.Fatalf("usage 端点失败应整体失败: %v", err)
	}

	// 币种显式声明:中转站点 *_usd 字段的货币语义随站点显示配置而变(new-api 可为 CNY),
	// 声明后以声明币种记账,数值换算口径不变(total_usage 仍按声明币种的百分之一)
	cny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/dashboard/billing/subscription":
			_, _ = w.Write([]byte(`{"hard_limit_usd":360.0}`))
		case "/v1/dashboard/billing/usage":
			_, _ = w.Write([]byte(`{"total_usage":0}`))
		}
	}))
	t.Cleanup(cny.Close)
	q, _ = For(SourceBillingCompat, SourceConfig{BaseURL: cny.URL, Currency: "CNY"})
	b, balErr = q.Query(context.Background(), "k")
	b = mustBalance(t, b, balErr)
	if b.Credits != 360_000_000_000 || b.Currency != "CNY" {
		t.Fatalf("声明币种应透传记账: %+v", b)
	}
}

func TestUpstreamCredits_截断与负值与拒收(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"110.00", 110_000_000_000, true},
		{"49.58894", 49_588_940_000, true},
		{"-0.50", -500_000_000, true},
		{"0.1234567890", 123_456_789, true}, // 10 位小数截断到 9 位
		{"1e3", 0, false},                   // 科学计数法拒收
		{"", 0, false},
		{".5", 0, false},
	}
	for _, c := range cases {
		got, err := upstreamCredits(json.Number(c.in))
		if c.ok && (err != nil || got != c.want) {
			t.Fatalf("upstreamCredits(%q) = %d, %v; 期望 %d", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Fatalf("upstreamCredits(%q) 应失败", c.in)
		}
	}
}

func TestQuery_超时经ctx承载(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	q, _ := For(SourceDeepseek, SourceConfig{BaseURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := q.Query(ctx, "k")
	if err == nil {
		t.Fatalf("超时应失败")
	}
	// ctx 截止必须穿透包装归类为 timeout(%w 保留错误链),不得退化为 network
	if classifyQueryErr(err) != ClassTimeout {
		t.Fatalf("超时应归 ClassTimeout,实际 %v", err)
	}
}
