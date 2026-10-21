package config

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 拓扑拉取源测试：控制面拉取与损坏快照拒绝。

const validPayload = `{
  "providers":[{"name":"alpha","adapter":"openai","base_url":"http://alpha",
    "keys":[{"env":"ALPHA_KEY","weight":1}],"weight":1,"priority":1}],
  "model_routes":{"m1":[{"provider":"alpha","model":"model-x","priority":1,"weight":1}]},
  "models":[{"provider":"alpha","model":"model-x","max_output_tokens":4096}]
}`

func TestFetchTopology_OK(t *testing.T) {
	var sawETag string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawETag = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"v7"`)
		_, _ = w.Write([]byte(validPayload))
	}))
	defer srv.Close()

	payload, etag, notModified, err := FetchTopology(srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	if notModified || etag != `"v7"` || len(payload.Providers) != 1 {
		t.Fatalf("拉取结果异常: notModified=%v etag=%q payload=%+v", notModified, etag, payload)
	}
	providers, routes, models := payload.ToTopology()
	if providers[0].Name != "alpha" || routes["m1"][0].Provider != "alpha" || len(models) != 1 {
		t.Fatalf("ToTopology 转换异常: %v %v %v", providers, routes, models)
	}
	// 条件请求：携带 ETag 且服务端 304
	if sawETag != "" {
		t.Fatal("首次拉取不应带 If-None-Match")
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != `"v7"` {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotModified)
	})
	_, _, notModified, err = FetchTopology(srv.Client(), srv.URL, `"v7"`)
	if err != nil || !notModified {
		t.Fatalf("304 条件请求异常: notModified=%v err=%v", notModified, err)
	}
}

func TestFetchTopology_RejectCorrupt(t *testing.T) {
	cases := map[string]string{
		"非法 JSON":     `{"providers":[`,
		"缺 routes":    `{"providers":[{"name":"a","adapter":"openai","base_url":"http://a","keys":[{"env":"K","weight":1}]}]}`,
		"缺 providers": `{"model_routes":{"m":[{"provider":"a","model":"x","priority":1}]}}`,
	}
	for name, payload := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(payload))
		}))
		_, _, _, err := FetchTopology(srv.Client(), srv.URL, "")
		srv.Close()
		if err == nil {
			t.Fatalf("%s: 损坏快照应拒绝应用", name)
		}
		if !strings.Contains(err.Error(), "拒绝") && !strings.Contains(err.Error(), "非法") && !strings.Contains(err.Error(), "不完整") {
			t.Fatalf("%s: 错误语义缺失: %v", name, err)
		}
	}
}

func TestFetchTopology_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, _, _, err := FetchTopology(srv.Client(), srv.URL, ""); err == nil {
		t.Fatal("上游 5xx 应报错(调用方保持旧快照)")
	}
}
