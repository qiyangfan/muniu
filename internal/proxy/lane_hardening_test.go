package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
)

const laneEmbOK = `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"up-emb","usage":{"prompt_tokens":1,"total_tokens":1}}`

func laneTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newLaneTestProxy(reg *adapter.Registry, routes map[string][]config.Candidate,
	pools map[string]*keypool.Pool, tmo Timeouts) *Proxy {
	return New(Config{
		Registry: reg, Routes: routes, Pools: pools, Timeouts: tmo,
		RandIntn: func(int) int { return 0 },
		Logger:   laneTestLogger(),
	})
}

// 首 key 429 后同 provider 换第二把 key;不允许直接耗尽候选。
func TestEmbeddings_同provider换key(t *testing.T) {
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") == "Bearer sk-bad" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		_, _ = io.WriteString(w, laneEmbOK)
	}))
	defer up.Close()
	reg := adapter.NewRegistry()
	reg.Register("p", adapter.NewOpenAI(up.URL, ""))
	pool := keypool.NewPool([]keypool.KeyInput{
		{ID: "bad", Value: "sk-bad", Weight: 1},
		{ID: "good", Value: "sk-good", Weight: 1},
	})
	p := newLaneTestProxy(reg,
		map[string][]config.Candidate{"emb": {{Provider: "p", Model: "up-emb", Weight: 1}}},
		map[string]*keypool.Pool{"p": pool},
		Timeouts{Dial: time.Second, FirstToken: 5 * time.Second})
	ts := httptest.NewServer(p.HandlerEmbeddings())
	defer ts.Close()
	code, body := foPost(t, ts, `{"model":"emb","input":"hi"}`)
	if code != http.StatusOK || body != laneEmbOK {
		t.Fatalf("同 provider 换 key 应成功: %d %s", code, body)
	}
	if hits.Load() != 2 {
		t.Fatalf("应两次上游尝试,实际 %d", hits.Load())
	}
	if st := pool.StateOf("bad", time.Now()); st != "cooldown" {
		t.Fatalf("429 key 应冷却,实际 %s", st)
	}
}

// 尝试上限是跨 lane 的:首个候选 5xx 后,第二候选在取 key 后、出站前被预算拦下。
func TestEmbeddings_预算尝试上限(t *testing.T) {
	var hitsA, hitsB atomic.Int64
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitsA.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitsB.Add(1)
		_, _ = io.WriteString(w, laneEmbOK)
	}))
	defer upB.Close()
	reg := adapter.NewRegistry()
	reg.Register("a", adapter.NewOpenAI(upA.URL, ""))
	reg.Register("b", adapter.NewOpenAI(upB.URL, ""))
	p := newLaneTestProxy(reg,
		map[string][]config.Candidate{"emb": {
			{Provider: "a", Model: "up-emb", Priority: 0, Weight: 1},
			{Provider: "b", Model: "up-emb", Priority: 1, Weight: 1},
		}},
		map[string]*keypool.Pool{
			"a": keypool.NewPool([]keypool.KeyInput{{ID: "ka", Value: "sk-a", Weight: 1}}),
			"b": keypool.NewPool([]keypool.KeyInput{{ID: "kb", Value: "sk-b", Weight: 1}}),
		},
		Timeouts{Dial: time.Second, FirstToken: 5 * time.Second, MaxAttempts: 1})
	ts := httptest.NewServer(p.HandlerEmbeddings())
	defer ts.Close()
	code, body := foPost(t, ts, `{"model":"emb","input":"hi"}`)
	if code != http.StatusGatewayTimeout || !strings.Contains(body, "failover_budget_exhausted") {
		t.Fatalf("尝试上限应 504 预算信封: %d %s", code, body)
	}
	if hitsA.Load() != 1 || hitsB.Load() != 0 {
		t.Fatalf("第二候选不得出站: A=%d B=%d", hitsA.Load(), hitsB.Load())
	}
}

func TestEmbeddings_池耗尽分级(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name       string
		report     func(*keypool.Pool)
		wantStatus int
		wantCode   string
	}{
		{
			name:       "全禁用503",
			report:     func(pool *keypool.Pool) { pool.Report(&keypool.Key{ID: "k"}, keypool.OutcomeAuthFailed, now, 0) },
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "keypool_exhausted",
		},
		{
			name: "全限频429",
			report: func(pool *keypool.Pool) {
				pool.Report(&keypool.Key{ID: "k"}, keypool.OutcomeRateLimited, now, time.Hour)
			},
			wantStatus: http.StatusTooManyRequests,
			wantCode:   "keypool_rate_limited",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer up.Close()
			reg := adapter.NewRegistry()
			reg.Register("p", adapter.NewOpenAI(up.URL, ""))
			pool := keypool.NewPool([]keypool.KeyInput{{ID: "k", Value: "sk", Weight: 1}})
			tc.report(pool)
			p := newLaneTestProxy(reg,
				map[string][]config.Candidate{"emb": {{Provider: "p", Model: "up-emb", Weight: 1}}},
				map[string]*keypool.Pool{"p": pool},
				Timeouts{Dial: time.Second, FirstToken: 5 * time.Second})
			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/embeddings",
				strings.NewReader(`{"model":"emb","input":"hi"}`))
			p.HandlerEmbeddings().ServeHTTP(w, r)
			if w.Code != tc.wantStatus || !strings.Contains(w.Body.String(), tc.wantCode) {
				t.Fatalf("池耗尽信封不符: %d %s", w.Code, w.Body.String())
			}
			if hits.Load() != 0 {
				t.Fatalf("池耗尽不得触上游: %d", hits.Load())
			}
			if tc.wantStatus == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
				t.Fatal("限频池耗尽应带 Retry-After")
			}
		})
	}
}

func TestImages_同provider换key(t *testing.T) {
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") == "Bearer sk-bad" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"url":"https://example.test/image"}]}`)
	}))
	defer up.Close()
	reg := adapter.NewRegistry()
	reg.Register("p", adapter.NewOpenAI(up.URL, ""))
	pool := keypool.NewPool([]keypool.KeyInput{
		{ID: "bad", Value: "sk-bad", Weight: 1},
		{ID: "good", Value: "sk-good", Weight: 1},
	})
	p := newLaneTestProxy(reg,
		map[string][]config.Candidate{"paint": {{Provider: "p", Model: "up-paint", Weight: 1}}},
		map[string]*keypool.Pool{"p": pool},
		Timeouts{Dial: time.Second, FirstToken: 5 * time.Second})
	ts := httptest.NewServer(p.HandlerImages())
	defer ts.Close()
	code, body := foPost(t, ts, `{"model":"paint","prompt":"x"}`)
	if code != http.StatusOK || !strings.Contains(body, "example.test") {
		t.Fatalf("images 同 provider 换 key 应成功: %d %s", code, body)
	}
	if hits.Load() != 2 {
		t.Fatalf("应两次上游尝试,实际 %d", hits.Load())
	}
}

func TestMultimodal_同provider换key(t *testing.T) {
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") == "Bearer sk-bad" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"output":{"embeddings":[]},"usage":{"input_tokens":1}}`)
	}))
	defer up.Close()
	endpoint := up.URL + multimodalPath
	qwen, err := adapter.For("qwen", "https://dashscope.invalid/v1", "", adapter.Options{NativeMultimodalEmbeddingURL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	reg := adapter.NewRegistry()
	reg.Register("qwen", qwen)
	pool := keypool.NewPool([]keypool.KeyInput{
		{ID: "bad", Value: "sk-bad", Weight: 1},
		{ID: "good", Value: "sk-good", Weight: 1},
	})
	p := newLaneTestProxy(reg,
		map[string][]config.Candidate{"alias": {{Provider: "qwen", Model: "qwen3-vl-embedding", Weight: 1}}},
		map[string]*keypool.Pool{"qwen": pool},
		Timeouts{Dial: time.Second, FirstToken: 5 * time.Second})
	ts := httptest.NewServer(p.HandlerMultimodalEmbeddings())
	defer ts.Close()
	code, body := foPost(t, ts, `{"model":"alias","input":{"contents":[{"text":"hi"}]}}`)
	if code != http.StatusOK || !strings.Contains(body, "embeddings") {
		t.Fatalf("multimodal 同 provider 换 key 应成功: %d %s", code, body)
	}
	if hits.Load() != 2 {
		t.Fatalf("应两次上游尝试,实际 %d", hits.Load())
	}
}

// 半开名额在 attempt panic 时也必须释放(server.Recover 回卷前 defer 已执行)。
func TestRunAttemptWithRelease_panic仍释放(t *testing.T) {
	released := false
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("测试夹具应触发 panic")
			}
		}()
		_, _ = runAttemptWithRelease(func() { released = true }, func() (bool, failover.ErrorClass) {
			panic("attempt panic")
		})
	}()
	if !released {
		t.Fatal("attempt panic 后释放函数未执行")
	}
}
