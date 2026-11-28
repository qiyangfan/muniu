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
	"github.com/solvela/muniu/internal/health"
	"github.com/solvela/muniu/internal/keypool"
)

// TestEmbeddings_渠道熔断跳过 非 chat lane 必须查渠道熔断态,降级渠道上游零调用。
func TestEmbeddings_渠道熔断跳过(t *testing.T) {
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	defer up.Close()
	hm := health.NewManager(context.Background(), health.Options{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		PassiveWindow: time.Minute, PassiveMinSamples: 3, PassiveRateMilli: 1000,
	})
	for i := 0; i < 5; i++ {
		hm.ProviderTraffic("prov-a", true)
	}
	if !hm.Contracted("prov-a") {
		t.Fatal("夹具应已进入降级态")
	}
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL, ""))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-e": {{Provider: "prov-a", Model: "emb-a", Priority: 0, Weight: 1}},
		},
		Pools:  map[string]*keypool.Pool{"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "k", Value: "sk-a"}})},
		Health: hm,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.HandlerEmbeddings())
	defer ts.Close()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/v1/embeddings", strings.NewReader(`{"model":"alias-e","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if hits.Load() != 0 {
		t.Fatalf("降级渠道不得被 embeddings lane 调用,实际 %d 次", hits.Load())
	}
}
