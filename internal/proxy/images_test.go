package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/adapter/bridge"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
)

func TestImages_候选与计费契约(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first       int
		broken      bool
		noPrice     bool
		budget      int64
		body        string
		wantStatus  int
		wantCalls   int64
		wantCost    int64
		wantRows    int
		unsupported bool
		allFailed   bool
	}{
		{name: "正常与上游usage无关", first: 201, body: `{"model":"paint","prompt":"x","n":2,"size":"1024","quality":"hd","extra":true}`, wantStatus: 201, wantCalls: 1, wantCost: 200, wantRows: 1},
		{name: "档位未中回落", first: 200, body: `{"model":"paint","prompt":"x","n":2}`, wantStatus: 200, wantCalls: 1, wantCost: 160, wantRows: 1},
		{name: "无价目零成本", first: 200, noPrice: true, body: `{"model":"paint","prompt":"x"}`, wantStatus: 200, wantCalls: 1, wantRows: 1},
		{name: "全部候选失败零账", first: 500, body: `{"model":"paint","prompt":"x"}`, wantStatus: 502, wantCalls: 2, wantRows: 2},
		{name: "5xx换道零扣失败", first: 500, body: `{"model":"paint","prompt":"x"}`, wantStatus: 200, wantCalls: 2, wantCost: 80, wantRows: 2},
		{name: "断流不交付半包", first: 200, broken: true, body: `{"model":"paint","prompt":"x"}`, wantStatus: 200, wantCalls: 2, wantCost: 80, wantRows: 2},
		{name: "预算精确拒绝", first: 200, budget: 79, body: `{"model":"paint","prompt":"x"}`, wantStatus: 402},
		{name: "预算等值放行", first: 200, budget: 80, body: `{"model":"paint","prompt":"x"}`, wantStatus: 200, wantCalls: 1, wantCost: 80, wantRows: 1},
		{name: "价缺失预算旁路", first: 200, budget: 1, noPrice: true, body: `{"model":"paint","prompt":"x"}`, wantStatus: 200, wantCalls: 1, wantRows: 1},
		{name: "非法请求", first: 200, body: `{"model":"paint"}`, wantStatus: 400},
		{name: "别名不存在", first: 200, body: `{"model":"missing","prompt":"x"}`, wantStatus: 400},
		{name: "能力不支持不换道", first: 200, unsupported: true, body: `{"model":"paint","prompt":"x"}`, wantStatus: 400, wantRows: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			handler := func(first bool) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.URL.Path != "/images/generations" {
						t.Errorf("路径 %s", r.URL.Path)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["model"] != "up-paint" {
						t.Errorf("模型未替换: %v", body)
					}
					if tc.broken && first {
						h, ok := w.(http.Hijacker)
						if !ok {
							t.Fatal("无 hijack")
						}
						conn, bw, err := h.Hijack()
						if err != nil {
							t.Fatal(err)
						}
						_, _ = bw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 999\r\n\r\npartial")
						_ = bw.Flush()
						_ = conn.Close()
						return
					}
					if (first && tc.first >= 400) || tc.name == "全部候选失败零账" {
						w.WriteHeader(tc.first)
						_, _ = io.WriteString(w, `{"error":{"message":"failed"}}`)
						return
					}
					if first {
						w.WriteHeader(tc.first)
					}
					_, _ = io.WriteString(w, `{"data":[{"url":"https://example.test/image"}],"usage":{"prompt_tokens":9999}}`)
				}
			}
			up1 := httptest.NewServer(handler(true))
			defer up1.Close()
			up2 := httptest.NewServer(handler(false))
			defer up2.Close()
			reg := adapter.NewRegistry()
			reg.Register("a", adapter.NewOpenAI(up1.URL, ""))
			reg.Register("b", adapter.NewOpenAI(up2.URL, ""))
			if tc.unsupported {
				reg.Register("a", bridge.NewAnthropic(up1.URL, adapter.Options{}))
			}
			db, err := store.Open(filepath.Join(t.TempDir(), "images.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			pipeline := metering.NewPipeline(db, metering.Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			var prices *metering.ImagePriceDirectory
			if !tc.noPrice {
				prices = metering.NewImagePriceDirectory(map[string]metering.ImagePrice{"paint": {Currency: "CNY", DefaultCredits: 80, Tiers: map[string]int64{"1024|hd": 100}}})
			}
			p := New(Config{Registry: reg, Routes: map[string][]config.Candidate{"paint": {{Provider: "a", Model: "up-paint", Weight: 1}, {Provider: "b", Model: "up-paint", Priority: 1, Weight: 1}}}, Pools: map[string]*keypool.Pool{"a": keypool.NewPool([]keypool.KeyInput{{ID: "a", Value: "x", Weight: 1}}), "b": keypool.NewPool([]keypool.KeyInput{{ID: "b", Value: "x", Weight: 1}})}, ImagePricing: prices, Pipeline: pipeline, Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second}, RandIntn: func(int) int { return 0 }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			r := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(tc.body))
			r = r.WithContext(auth.WithKeyInfo(r.Context(), &auth.KeyInfo{MaxRequestCostCredits: tc.budget}))
			w := httptest.NewRecorder()
			p.HandlerImages().ServeHTTP(w, r)
			if err := pipeline.Close(); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.wantStatus || calls.Load() != tc.wantCalls {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
			}
			var n int
			var cost, tokens, estimated int64
			if err := db.QueryRow(`SELECT count(*),coalesce(sum(cost_credits),0),coalesce(sum(prompt_tokens+completion_tokens+reasoning_tokens+cached_tokens),0),coalesce(sum(estimated),0) FROM usage_logs`).Scan(&n, &cost, &tokens, &estimated); err != nil {
				t.Fatal(err)
			}
			if n != tc.wantRows || cost != tc.wantCost || tokens != 0 || estimated != 0 {
				t.Fatalf("rows=%d cost=%d tokens=%d estimated=%d", n, cost, tokens, estimated)
			}
			if tc.broken && strings.Contains(w.Body.String(), "partial") {
				t.Fatal("半包泄漏")
			}
		})
	}
}

type imageTransportFunc func(*http.Request) (*http.Response, error)

func (f imageTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type imageCancelReader struct {
	ctx     context.Context
	started chan struct{}
}

func (r *imageCancelReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
func (r *imageCancelReader) Close() error { return nil }

func TestImages_响应读取期间取消不换道(t *testing.T) {
	started := make(chan struct{})
	ad := adapter.NewOpenAI("http://fixture.test", "")
	p := New(Config{Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second}})
	p.client = &http.Client{Transport: imageTransportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: &imageCancelReader{ctx: r.Context(), started: started}}, nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("POST", "/v1/images/generations", nil).WithContext(ctx)
	tw := &trackWriter{ResponseWriter: httptest.NewRecorder()}
	m := &meterState{start: time.Now(), status: "ok"}
	result := make(chan failover.ErrorClass, 1)
	go func() {
		_, class := p.attemptImages(tw, r, ad, failover.Candidate{Provider: "a", Model: "paint"}, &keypool.Key{Value: "x"}, &protocol.ImageRequest{Model: "paint", Prompt: "x", N: 1, Raw: []byte(`{"model":"paint","prompt":"x"}`)}, m)
		result <- class
	}()
	<-started
	cancel()
	if class := <-result; class != failover.ClassContextCanceled || m.status != "client_cancel" {
		t.Fatalf("取消误分类: %s %s", class, m.status)
	}
	if tw.committed {
		t.Fatal("取消后提交响应")
	}
}
