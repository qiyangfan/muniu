// 模型发现契约：列表与拓扑快照同源，热替换同步可见；id 升序；
// 单查命中 200、未命中 404 not_found 信封；created 为快照加载时刻。
package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/protocol"
)

func modelsMux(p *Proxy) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /v1/models", p.HandlerModels())
	mux.Handle("GET /v1/models/{model}", p.HandlerModels())
	return mux
}

func TestModelCatalog_列表与快照同源且升序(t *testing.T) {
	cfg := topoConfig("alpha", "http://alpha")
	cfg.Routes["zz"] = cfg.Routes["m1"]
	cfg.Routes["aa"] = cfg.Routes["m1"]
	p := New(cfg)

	rec := httptest.NewRecorder()
	modelsMux(p).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200: %d", rec.Code)
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "list" || len(body.Data) != 3 {
		t.Fatalf("形态不符: %+v", body)
	}
	if body.Data[0].ID != "aa" || body.Data[1].ID != "m1" || body.Data[2].ID != "zz" {
		t.Fatalf("应按 id 升序: %v", body.Data)
	}
	for _, e := range body.Data {
		if e.Object != "model" || e.OwnedBy != "muniu" || e.Created == 0 {
			t.Fatalf("条目字段不符: %+v", e)
		}
	}
}

// 热替换后列表同步变化：目录与转发同源，零额外失效机制。
func TestModelCatalog_热替换同步(t *testing.T) {
	cfg := topoConfig("alpha", "http://alpha")
	p := New(cfg)
	next := topoConfig("beta", "http://beta")
	next.Routes = map[string][]config.Candidate{
		"beta-alias": {{Provider: "beta", Model: "model-x", Priority: 1}},
	}
	if _, err := p.ReplaceTopology(next, buildOK); err != nil {
		t.Fatal(err)
	}
	aliases, _ := p.ModelAliases()
	if len(aliases) != 1 || aliases[0] != "beta-alias" {
		t.Fatalf("别名目录应随快照更新: %v", aliases)
	}
	rec := httptest.NewRecorder()
	modelsMux(p).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if !containsAll(rec.Body.String(), `"id":"beta-alias"`) || containsAll(rec.Body.String(), `"id":"m1"`) {
		t.Fatalf("热替换后应只含新目录: %s", rec.Body.String())
	}
}

func TestModelCatalog_单查命中与未命中404(t *testing.T) {
	p := New(topoConfig("alpha", "http://alpha"))
	mux := modelsMux(p)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/m1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("命中应 200: %d", rec.Code)
	}
	if !containsAll(rec.Body.String(), `"id":"m1"`, `"object":"model"`) {
		t.Fatalf("单查形态不符: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未命中应 404: %d", rec.Code)
	}
	var body struct {
		Error protocol.ErrorBody `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != protocol.TypeNotFound || body.Error.Code != "model_not_found" {
		t.Fatalf("信封应为 not_found/model_not_found: %+v", body.Error)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
