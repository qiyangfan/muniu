package proxy

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/metrics"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

const multimodalPath = "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"

func TestMultimodalEmbeddings_不完整Usage保留已知总token并标估算(t *testing.T) {
	const responseBody = `{"output":{"embeddings":[]},"usage":{"input_tokens":13}}`
	p, db := newMeteredMultimodalProxy(t, "tongyi-embedding-vision-flash", responseBody)
	w := httptest.NewRecorder()
	p.HandlerMultimodalEmbeddings().ServeHTTP(w, httptest.NewRequest(http.MethodPost, multimodalPath,
		strings.NewReader(`{"model":"alias","input":{"contents":[{"text":"hi"}]}}`)))
	if w.Code != http.StatusOK || w.Body.String() != responseBody {
		t.Fatalf("原生响应必须逐字节透传: status=%d body=%q", w.Code, w.Body.String())
	}
	rows := waitMultimodalRows(t, db, 1)
	if rows[0].PromptTokens != 13 || rows[0].VisionTokens != 0 || !rows[0].Estimated {
		t.Fatalf("缺视觉明细时须保留已知总量并标估算: %+v", rows[0])
	}
}

func TestMultimodalEmbeddings_E2E视觉用量按视觉价结算(t *testing.T) {
	const responseBody = `{"output":{"embeddings":[]},"usage":{"input_tokens":10,"image_tokens":3,"total_tokens":13}}`
	p, db := newMeteredMultimodalProxy(t, "qwen3-vl-embedding", responseBody)
	w := httptest.NewRecorder()
	p.HandlerMultimodalEmbeddings().ServeHTTP(w, httptest.NewRequest(http.MethodPost, multimodalPath,
		strings.NewReader(`{"model":"alias","input":{"contents":[{"text":"hi"}]}}`)))
	if w.Code != http.StatusOK || w.Body.String() != responseBody {
		t.Fatalf("原生响应必须逐字节透传: status=%d body=%q", w.Code, w.Body.String())
	}
	rows := waitMultimodalRows(t, db, 1)
	if rows[0].PromptTokens != 13 || rows[0].VisionTokens != 3 || rows[0].CostCredits != 16 || rows[0].Estimated {
		t.Fatalf("完整链路必须保留视觉 token 并按文本/视觉单价计费: %+v", rows[0])
	}
}

func newMeteredMultimodalProxy(t *testing.T, model, responseBody string) (*Proxy, *sql.DB) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(up.Close)
	endpoint := up.URL + multimodalPath
	qwen, err := adapter.For("qwen", "https://dashscope.invalid/v1", "", adapter.Options{NativeMultimodalEmbeddingURL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	reg := adapter.NewRegistry()
	reg.Register("qwen", qwen)
	db, err := store.Open(t.TempDir() + "/usage.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{Batch: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = pipeline.Close() })
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "qwen", Adapter: "qwen", NativeMultimodalEmbeddingsURL: endpoint}},
		Routes:    map[string][]config.Candidate{"alias": {{Provider: "qwen", Model: model, Weight: 1}}},
		Pools:     map[string]*keypool.Pool{"qwen": keypool.NewPool([]keypool.KeyInput{{ID: "k", Value: "key", Weight: 1}})},
		Pipeline:  pipeline,
		Pricing: mustDir(t, map[string]metering.Pricing{"alias": {
			Currency: "CNY", InputCreditsPerM: 1_000_000, VisionInputCreditsPerM: 2_000_000,
		}}),
		Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return p, db
}

func waitMultimodalRows(t *testing.T, db *sql.DB, want int) []store.UsageRow {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := db.Query(`SELECT prompt_tokens, vision_tokens, estimated, cost_credits FROM usage_logs`)
		if err != nil {
			t.Fatal(err)
		}
		var got []store.UsageRow
		for rows.Next() {
			var row store.UsageRow
			if err := rows.Scan(&row.PromptTokens, &row.VisionTokens, &row.Estimated, &row.CostCredits); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			got = append(got, row)
		}
		_ = rows.Close()
		if len(got) >= want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 %d 条用量行超时", want)
	return nil
}

func TestMultimodalEmbeddings_只走声明能力并原生透传(t *testing.T) {
	var unsupportedCalls atomic.Int64
	unsupported := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		unsupportedCalls.Add(1)
		_, _ = io.WriteString(w, `{"wrong":"openai embeddings lane"}`)
	}))
	defer unsupported.Close()
	const responseBody = `{"output":{"embeddings":[{"embedding":[0.1,0.2]}]},"usage":{"input_tokens":10,"image_tokens":3,"total_tokens":13}}`
	var receivedBody, receivedPath, receivedAuth string
	supported := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody, receivedPath, receivedAuth = string(b), r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Connection", "X-Hop")
		w.Header().Set("X-Hop", "must-remove")
		w.Header().Set("X-Trace", "kept")
		w.Header().Set("X-Multi", "one")
		w.Header().Add("X-Multi", "two")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer supported.Close()

	reg := adapter.NewRegistry()
	reg.Register("unsupported", adapter.NewOpenAI(unsupported.URL, ""))
	qwen, err := adapter.For("qwen", "https://dashscope.invalid/compatible-mode/v1", "", adapter.Options{
		NativeMultimodalEmbeddingURL: supported.URL + multimodalPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	reg.Register("supported", qwen)
	providers := []config.Provider{
		{Name: "unsupported", Adapter: "openai"},
		{Name: "supported", Adapter: "qwen", NativeMultimodalEmbeddingsURL: supported.URL + multimodalPath},
	}
	p := New(Config{
		Registry: reg, Providers: providers,
		Routes: map[string][]config.Candidate{"alias": {
			{Provider: "unsupported", Model: "up-openai", Priority: 0, Weight: 1},
			{Provider: "supported", Model: "qwen3-vl-embedding", Priority: 1, Weight: 1},
		}},
		Pools: map[string]*keypool.Pool{
			"unsupported": keypool.NewPool([]keypool.KeyInput{{ID: "u", Value: "u-key", Weight: 1}}),
			"supported":   keypool.NewPool([]keypool.KeyInput{{ID: "q", Value: "qwen-key", Weight: 1}}),
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second},
		RandIntn: func(int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	body := "{\n  \"model\": \"alias\",\n  \"input\": {\"contents\": [{\"text\": \"hi\"}]},\n  \"parameters\": {\"enable_fusion\": true}, \"unknown\": [1, 2]\n}"
	req := httptest.NewRequest(http.MethodPost, multimodalPath, strings.NewReader(body))
	w := httptest.NewRecorder()
	p.HandlerMultimodalEmbeddings().ServeHTTP(w, req)
	if w.Code != http.StatusCreated || w.Body.String() != responseBody {
		t.Fatalf("上游状态/body 应原样返回: %d %s", w.Code, w.Body.String())
	}
	parsed, err := protocol.ParseMultimodalEmbeddingRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	wantBody, err := protocol.RewriteModelInPlace(parsed.Raw, "qwen3-vl-embedding")
	if err != nil {
		t.Fatal(err)
	}
	if receivedPath != multimodalPath || receivedAuth != "Bearer qwen-key" || receivedBody != string(wantBody) {
		t.Fatalf("出站请求不符: path=%q auth=%q body=%s", receivedPath, receivedAuth, receivedBody)
	}
	if unsupportedCalls.Load() != 0 {
		t.Fatalf("未声明能力的候选被触达: %d", unsupportedCalls.Load())
	}
	if w.Header().Get("X-Trace") != "kept" || strings.Join(w.Header().Values("X-Multi"), ",") != "one,two" || w.Header().Get("X-Hop") != "" || w.Header().Get("Connection") != "" {
		t.Fatalf("响应端到端头过滤不符: %v", w.Header())
	}
}

func TestMultimodalEmbeddings_无能力候选本地拒绝(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer up.Close()
	reg := adapter.NewRegistry()
	reg.Register("openai", adapter.NewOpenAI(up.URL, ""))
	p := New(Config{
		Registry: reg,
		Routes:   map[string][]config.Candidate{"alias": {{Provider: "openai", Model: "upstream", Weight: 1}}},
		Pools:    map[string]*keypool.Pool{"openai": keypool.NewPool([]keypool.KeyInput{{ID: "k", Value: "key", Weight: 1}})},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	w := httptest.NewRecorder()
	p.HandlerMultimodalEmbeddings().ServeHTTP(w, httptest.NewRequest(http.MethodPost, multimodalPath,
		strings.NewReader(`{"model":"alias","input":{"contents":[{"text":"hi"}]}}`)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "multimodal_embeddings_not_supported") || calls.Load() != 0 {
		t.Fatalf("无能力候选应本地拒绝且零上游: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
}

func TestMultimodalEmbeddings_不支持模型不触达上游(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer up.Close()
	endpoint := up.URL + multimodalPath
	qwen, err := adapter.For("qwen", "https://dashscope.invalid/v1", "", adapter.Options{NativeMultimodalEmbeddingURL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	reg := adapter.NewRegistry()
	reg.Register("qwen", qwen)
	p := New(Config{
		Registry: reg, Providers: []config.Provider{{Name: "qwen", Adapter: "qwen", NativeMultimodalEmbeddingsURL: endpoint}},
		Routes:   map[string][]config.Candidate{"alias": {{Provider: "qwen", Model: "qwen-plus", Weight: 1}}},
		Pools:    map[string]*keypool.Pool{"qwen": keypool.NewPool([]keypool.KeyInput{{ID: "key", Value: "key", Weight: 1}})},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	w := httptest.NewRecorder()
	p.HandlerMultimodalEmbeddings().ServeHTTP(w, httptest.NewRequest(http.MethodPost, multimodalPath,
		strings.NewReader(`{"model":"alias","input":{"contents":[{"text":"hi"}]}}`)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "multimodal_embeddings_not_supported") || calls.Load() != 0 {
		t.Fatalf("当前 lane 外的上游模型应本地拒绝: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
}

func TestMultimodalEmbeddings_白名单先于插件和上游(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"output":{"embeddings":[]},"usage":{"input_tokens":1,"image_tokens":0}}`)
	}))
	defer up.Close()
	endpoint := up.URL + multimodalPath
	ad, err := adapter.For("qwen", "https://dashscope.invalid/v1", "", adapter.Options{NativeMultimodalEmbeddingURL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	reg := adapter.NewRegistry()
	reg.Register("qwen", ad)
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "qwen", Adapter: "qwen", NativeMultimodalEmbeddingsURL: endpoint}},
		Routes:    map[string][]config.Candidate{"alias": {{Provider: "qwen", Model: "qwen3-vl-embedding", Weight: 1}}},
		Pools:     map[string]*keypool.Pool{"qwen": keypool.NewPool([]keypool.KeyInput{{ID: "k", Value: "key", Weight: 1}})},
		Timeouts:  Timeouts{Dial: time.Second, FirstToken: time.Second},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	request := func(allowed []string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, multimodalPath, strings.NewReader(`{"model":"alias","input":{"contents":[{"text":"hi"}]}}`))
		r = r.WithContext(auth.WithKeyInfo(r.Context(), &auth.KeyInfo{AllowedModels: allowed}))
		w := httptest.NewRecorder()
		p.HandlerMultimodalEmbeddings().ServeHTTP(w, r)
		return w
	}
	denied := request([]string{"other"})
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "model_not_allowed") || calls.Load() != 0 {
		t.Fatalf("白名单外必须先拒绝: status=%d calls=%d body=%s", denied.Code, calls.Load(), denied.Body.String())
	}
	allowed := request([]string{"alias"})
	if allowed.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("白名单内应转发: status=%d calls=%d body=%s", allowed.Code, calls.Load(), allowed.Body.String())
	}
}

func TestMultimodalEmbeddings_插件改写后重路由复校验白名单(t *testing.T) {
	metrics.SetEnabled(true)
	t.Cleanup(func() { metrics.SetEnabled(false) })
	reg := manualRegistry(t, "multimodal-model-revalidate", testkit.FixtureModifyMultimodalModel, 200)
	var calls atomic.Int64
	var receivedModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		receivedModel = body.Model
		_, _ = io.WriteString(w, `{"output":{"embeddings":[]},"usage":{"input_tokens":1,"image_tokens":0}}`)
	}))
	defer up.Close()
	endpoint := up.URL + multimodalPath
	qwen, err := adapter.For("qwen", "https://dashscope.invalid/v1", "", adapter.Options{NativeMultimodalEmbeddingURL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	adapters := adapter.NewRegistry()
	adapters.Register("qwen", qwen)
	p := New(Config{
		Registry:  adapters,
		Providers: []config.Provider{{Name: "qwen", Adapter: "qwen", NativeMultimodalEmbeddingsURL: endpoint}},
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "qwen", Model: "qwen3-vl-embedding", Weight: 1}},
			"alias-b": {{Provider: "qwen", Model: "tongyi-embedding-vision-flash", Weight: 1}},
		},
		Pools:   map[string]*keypool.Pool{"qwen": keypool.NewPool([]keypool.KeyInput{{ID: "k", Value: "key", Weight: 1}})},
		Plugins: reg, Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	r := httptest.NewRequest(http.MethodPost, multimodalPath,
		strings.NewReader(`{"model":"alias-a","input":{"contents":[{"text":"hi"}]}}`))
	r = r.WithContext(auth.WithKeyInfo(r.Context(), &auth.KeyInfo{AllowedModels: []string{"alias-a"}}))
	w := httptest.NewRecorder()
	p.HandlerMultimodalEmbeddings().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "model_not_allowed") || calls.Load() != 0 {
		t.Fatalf("插件改写到白名单外 alias-b 必须在出站前拒绝: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	var rendered strings.Builder
	metrics.RenderTo(&rendered)
	if !strings.Contains(rendered.String(), `muniu_plugin_calls_total{plugin="multimodal-model-revalidate",verdict="modified"} 1`) {
		t.Fatalf("插件必须只执行一次: %s", rendered.String())
	}
	if receivedModel != "" {
		t.Fatalf("白名单拒绝后不得构造上游请求，实际 model=%q", receivedModel)
	}
}

func TestMultimodalEmbeddings_耗尽时返回最后上游错误(t *testing.T) {
	responses := []struct {
		status int
		header string
		body   string
	}{
		{http.StatusServiceUnavailable, "first", `{"error":"first"}`},
		{http.StatusUnprocessableEntity, "last", ` { "error" : "last" } `},
	}
	servers := make([]*httptest.Server, len(responses))
	reg := adapter.NewRegistry()
	providers := make([]config.Provider, len(responses))
	pools := make(map[string]*keypool.Pool, len(responses))
	candidates := make([]config.Candidate, len(responses))
	for i, response := range responses {
		i, response := i, response
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Upstream", response.header)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(response.status)
			_, _ = io.WriteString(w, response.body)
		}))
		defer servers[i].Close()
		name := "qwen-" + string(rune('a'+i))
		endpoint := servers[i].URL + multimodalPath
		ad, err := adapter.For("qwen", "https://dashscope.invalid/v1", "", adapter.Options{NativeMultimodalEmbeddingURL: endpoint})
		if err != nil {
			t.Fatal(err)
		}
		reg.Register(name, ad)
		providers[i] = config.Provider{Name: name, Adapter: "qwen", NativeMultimodalEmbeddingsURL: endpoint}
		pools[name] = keypool.NewPool([]keypool.KeyInput{{ID: name, Value: "key", Weight: 1}})
		candidates[i] = config.Candidate{Provider: name, Model: "qwen3-vl-embedding", Priority: i, Weight: 1}
	}
	p := New(Config{
		Registry: reg, Providers: providers,
		Routes: map[string][]config.Candidate{"alias": candidates}, Pools: pools,
		Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second},
		RandIntn: func(int) int { return 0 }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	w := httptest.NewRecorder()
	p.HandlerMultimodalEmbeddings().ServeHTTP(w, httptest.NewRequest(http.MethodPost, multimodalPath,
		strings.NewReader(`{"model":"alias","input":{"contents":[{"text":"hi"}]}}`)))
	if w.Code != http.StatusUnprocessableEntity || w.Body.String() != responses[1].body || w.Header().Get("X-Upstream") != "last" {
		t.Fatalf("应透传最后一个上游错误: status=%d header=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

func TestMultimodalEmbeddings_耗尽时保留非4xx5xx上游响应(t *testing.T) {
	const responseBody = `{"error":"multiple choices"}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Upstream", "multiple-choices")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMultipleChoices)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer up.Close()

	endpoint := up.URL + multimodalPath
	ad, err := adapter.For("qwen", "https://dashscope.invalid/v1", "", adapter.Options{NativeMultimodalEmbeddingURL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	reg := adapter.NewRegistry()
	reg.Register("qwen", ad)
	p := New(Config{
		Registry: reg, Providers: []config.Provider{{Name: "qwen", Adapter: "qwen", NativeMultimodalEmbeddingsURL: endpoint}},
		Routes:   map[string][]config.Candidate{"alias": {{Provider: "qwen", Model: "qwen3-vl-embedding", Weight: 1}}},
		Pools:    map[string]*keypool.Pool{"qwen": keypool.NewPool([]keypool.KeyInput{{ID: "key", Value: "key", Weight: 1}})},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	w := httptest.NewRecorder()
	p.HandlerMultimodalEmbeddings().ServeHTTP(w, httptest.NewRequest(http.MethodPost, multimodalPath,
		strings.NewReader(`{"model":"alias","input":{"contents":[{"text":"hi"}]}}`)))
	if w.Code != http.StatusMultipleChoices || w.Body.String() != responseBody || w.Header().Get("X-Upstream") != "multiple-choices" {
		t.Fatalf("候选耗尽时应保留最后上游 HTTP 响应: status=%d header=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

func TestMultimodalEmbeddings_不跟随上游重定向(t *testing.T) {
	var redirectedCalls atomic.Int64
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedCalls.Add(1)
		_, _ = io.WriteString(w, `{"target":"must not be fetched"}`)
	}))
	defer redirectTarget.Close()

	const responseBody = `{"redirect":"preserved"}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", redirectTarget.URL)
		w.Header().Set("X-Upstream", "redirect")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusFound)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer up.Close()

	endpoint := up.URL + multimodalPath
	ad, err := adapter.For("qwen", "https://dashscope.invalid/v1", "", adapter.Options{NativeMultimodalEmbeddingURL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	reg := adapter.NewRegistry()
	reg.Register("qwen", ad)
	p := New(Config{
		Registry: reg, Providers: []config.Provider{{Name: "qwen", Adapter: "qwen", NativeMultimodalEmbeddingsURL: endpoint}},
		Routes:   map[string][]config.Candidate{"alias": {{Provider: "qwen", Model: "qwen3-vl-embedding", Weight: 1}}},
		Pools:    map[string]*keypool.Pool{"qwen": keypool.NewPool([]keypool.KeyInput{{ID: "key", Value: "key", Weight: 1}})},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	w := httptest.NewRecorder()
	p.HandlerMultimodalEmbeddings().ServeHTTP(w, httptest.NewRequest(http.MethodPost, multimodalPath,
		strings.NewReader(`{"model":"alias","input":{"contents":[{"text":"hi"}]}}`)))
	if w.Code != http.StatusFound || w.Body.String() != responseBody || w.Header().Get("X-Upstream") != "redirect" || redirectedCalls.Load() != 0 {
		t.Fatalf("原生上游重定向必须原样透传且不得跟随: status=%d header=%v body=%q target_calls=%d",
			w.Code, w.Header(), w.Body.String(), redirectedCalls.Load())
	}
}

func TestMultimodalEmbeddings_压缩响应保持原始编码(t *testing.T) {
	const responseBody = `{"output":{"embeddings":[]}}`
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := io.WriteString(gz, responseBody); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprint(compressed.Len()))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(compressed.Bytes())
	}))
	defer up.Close()
	endpoint := up.URL + multimodalPath
	ad, err := adapter.For("qwen", "https://dashscope.invalid/v1", "", adapter.Options{NativeMultimodalEmbeddingURL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	reg := adapter.NewRegistry()
	reg.Register("qwen", ad)
	p := New(Config{
		Registry: reg, Providers: []config.Provider{{Name: "qwen", Adapter: "qwen", NativeMultimodalEmbeddingsURL: endpoint}},
		Routes:   map[string][]config.Candidate{"alias": {{Provider: "qwen", Model: "qwen3-vl-embedding", Weight: 1}}},
		Pools:    map[string]*keypool.Pool{"qwen": keypool.NewPool([]keypool.KeyInput{{ID: "key", Value: "key", Weight: 1}})},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	w := httptest.NewRecorder()
	p.HandlerMultimodalEmbeddings().ServeHTTP(w, httptest.NewRequest(http.MethodPost, multimodalPath,
		strings.NewReader(`{"model":"alias","input":{"contents":[{"text":"hi"}]}}`)))
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), compressed.Bytes()) ||
		w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Length") != fmt.Sprint(compressed.Len()) {
		t.Fatalf("原生压缩响应必须保持原始 body 与编码头: status=%d headers=%v body=%x want=%x",
			w.Code, w.Header(), w.Body.Bytes(), compressed.Bytes())
	}
}
