// managed 端到端：签名快照应用（价目/拓扑/吊销/租户）、注册心跳、用量上报、
// 命令 claim/ack、managed 共享控制只读、服务只读 API 与本地命令。
package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/testkit"
)

const managedServiceToken = "svc-token-0123456789abcdef"
const managedReadToken = "read-token-0123456789abcdef"

// controlPlaneServer 模拟锦官：签名快照 + 注册心跳 + 用量 + 命令。
type controlPlaneServer struct {
	*httptest.Server
	key         *rsa.PrivateKey
	snapshot    []byte
	etag        string
	registers   atomic.Int64
	heartbeats  atomic.Int64
	usage       atomic.Value
	claims      atomic.Int64
	acks        atomic.Value
	authMissing atomic.Bool
}

func newControlPlaneServer(t *testing.T, snapshot []byte) *controlPlaneServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	c := &controlPlaneServer{key: key, snapshot: snapshot}
	c.etag = `"` + sha256Hex(snapshot)[:16] + `"`
	c.Server = httptest.NewServer(http.HandlerFunc(c.handle))
	t.Cleanup(c.Close)
	return c
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (c *controlPlaneServer) sign(body []byte) string {
	h := sha256.Sum256(body)
	sig, _ := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, h[:])
	return base64.StdEncoding.EncodeToString(sig)
}

func (c *controlPlaneServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+managedServiceToken {
		c.authMissing.Store(true)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/control/v1/snapshot":
		if r.Header.Get("If-None-Match") == c.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", c.etag)
		w.Header().Set("X-Control-Signature", c.sign(c.snapshot))
		_, _ = w.Write(c.snapshot)
	case r.URL.Path == "/control/v1/registry/instances":
		c.registers.Add(1)
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/heartbeat"):
		c.heartbeats.Add(1)
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/ingest/usage":
		raw, _ := io.ReadAll(r.Body)
		c.usage.Store(string(raw))
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/control/v1/commands/claim":
		if c.claims.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"commands": []any{
				map[string]any{"id": "cmd-1", "action": "cache_purge", "action_schema_version": 1,
					"params": map[string]any{"all": true}, "idempotency_key": "idem-1",
					"actor": "ops", "reason": "routine"},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"commands": []any{}})
	case strings.HasSuffix(r.URL.Path, "/ack"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.acks.Store(body)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// managedSnapshot 构造首版快照：拓扑指向 fake 上游、价目/租户/吊销齐备。
func managedSnapshot(t *testing.T, upstreamURL string) []byte {
	t.Helper()
	env := struct {
		Version    int64     `json:"version"`
		ObservedAt time.Time `json:"observed_at"`
		Payload    any       `json:"payload"`
	}{
		Version: 1, ObservedAt: time.Now().UTC(),
		Payload: map[string]any{
			"pricing": []map[string]any{{
				"alias": "alias-a", "currency": "CNY", "input": "2.00", "cached_input": "",
				"vision_input": "", "output": "8.00", "timezone": "UTC", "time_windows": []any{},
			}},
			"topology": map[string]any{
				"providers": []map[string]any{{
					"name": "openai", "adapter": "openai", "base_url": upstreamURL,
					"keys": []map[string]any{{"env": "E2E_FAKE_KEY"}}, "weight": 10, "priority": 0,
				}},
				"model_routes": map[string]any{
					"alias-a": []map[string]any{{"provider": "openai", "model": "up-a", "priority": 0}},
				},
				"models": []any{},
			},
			"revocations": map[string]any{"version": 1, "revoked": []string{}},
			"tenants": []map[string]any{{
				"id": "01900000-0000-7000-8000-000000000001", "name": "default",
				"status": "active", "rpm_limit": 0, "tpm_limit": 0, "daily_quota_credits": 0,
			}},
		},
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writePublicPEM(t *testing.T, pub *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "control-pub.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func managedConfigYAML(t *testing.T, dbPath, upstreamURL, controlURL, pubPath string) string {
	t.Helper()
	return `
server:
  addr: ":0"
log:
  level: error
  format: text
health:
  enabled: false
storage:
  sqlite:
    path: "` + dbPath + `"
metering:
  flush_interval_ms: 10
  flush_batch: 1
control_plane:
  mode: managed
  base_url: "` + controlURL + `"
  service_token: "` + managedServiceToken + `"
  snapshot_public_key_file: "` + pubPath + `"
  poll_interval_sec: 1
  heartbeat_interval_sec: 1
  command_poll_interval_sec: 1
  request_timeout_sec: 2
service_api:
  token: "` + managedReadToken + `"
providers:
  - name: openai
    adapter: openai
    base_url: "` + upstreamURL + `"
    keys: [ { env: E2E_FAKE_KEY } ]
    weight: 10
model_routes:
  alias-a:
    - { provider: openai, model: up-a, priority: 0 }
pricing:
  alias-a:
    currency: CNY
    input: "2.00"
    output: "8.00"
`
}

func waitJSON(t *testing.T, url, token string, cond func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			_ = json.Unmarshal(raw, &last)
			if cond(last) {
				return last
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待条件超时,最后响应: %v", last)
	return nil
}

func TestManagedE2E_快照注册用量命令与只读面(t *testing.T) {
	t.Setenv("E2E_FAKE_KEY", "sk-e2e")
	t.Setenv("MUNIU_ADMIN_TOKEN", "admin-token")
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	snap := managedSnapshot(t, up.URL())
	cp := newControlPlaneServer(t, snap)
	pubPath := writePublicPEM(t, &cp.key.PublicKey)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "muniu.db")
	cfgPath := filepath.Join(dir, "muniu.yaml")
	cfgYAML := managedConfigYAML(t, dbPath, up.URL(), cp.URL, pubPath)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	seedDB, err := config0Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, _, err := auth.CreateKey(context.Background(), seedDB, "default", "managed-e2e", 60, auth.KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	_ = seedDB.Close()

	handler, routes, cleanup, err := assemble(context.Background(), cfg, cfgPath,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("managed 装配失败: %v", err)
	}
	t.Cleanup(cleanup)
	if !containsRoute(routes, "GET /service/v1/summary") {
		t.Fatal("service_api 配置后必须注册只读路由")
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	status := waitJSON(t, ts.URL+"/admin/v1/runtime/status", "admin-token", func(m map[string]any) bool {
		snap, _ := m["snapshot"].(map[string]any)
		reg, _ := m["registration"].(map[string]any)
		return snap != nil && snap["ready"] == true && reg["state"] == "ok"
	})
	if status["mode"] != "managed" {
		t.Fatalf("状态模式不符: %v", status)
	}

	// 数据面：快照拓扑 + 价目生效
	resp := postChat(t, ts.URL, plaintext)
	if resp != 200 {
		t.Fatalf("managed 数据面应 200: %d", resp)
	}
	if !waitUsage(cp, "alias-a") {
		t.Fatal("用量必须上报锦官且 source=muniu")
	}
	if cp.registers.Load() == 0 || cp.heartbeats.Load() == 0 {
		t.Fatalf("注册/心跳未上报: reg=%d hb=%d", cp.registers.Load(), cp.heartbeats.Load())
	}
	if cp.authMissing.Load() {
		t.Fatal("控制面调用必须携带服务身份")
	}

	// managed 共享控制只读
	if code, _ := adminCall(t, ts.URL, "PUT", "/admin/v1/pricing/alias-a", "admin-token",
		`{"currency":"CNY","input":"1","output":"1","timezone":"UTC"}`); code != 403 {
		t.Fatalf("managed 价目写必须 403: %d", code)
	}
	if code, _ := adminCall(t, ts.URL, "POST", "/admin/v1/tenants", "admin-token", `{"name":"x"}`); code != 403 {
		t.Fatalf("managed 租户写必须 403: %d", code)
	}
	if code, _ := adminCall(t, ts.URL, "PUT", "/admin/v1/image-pricing/gpt-image", "admin-token",
		`{"currency":"CNY","default":"0.1","tiers":{}}`); code != 403 {
		t.Fatalf("managed 图片价目写必须 403: %d", code)
	}
	if code, _ := adminCall(t, ts.URL, "POST", "/admin/v1/keys", "admin-token",
		`{"tenant":"ghost","name":"k","rpm_limit":1}`); code != 400 {
		t.Fatalf("managed 未知租户签 key 必须 400: %d", code)
	}
	if code, body := adminCall(t, ts.URL, "POST", "/admin/v1/keys", "admin-token",
		`{"tenant":"default","name":"k2","rpm_limit":1}`); code != 200 {
		t.Fatalf("服务域 key 签发应允许: %d %s", code, body)
	}

	// 命令 claim/ack
	waitJSON(t, ts.URL+"/admin/v1/runtime/commands", "admin-token", func(m map[string]any) bool {
		stats, _ := m["stats"].(map[string]any)
		v, _ := stats["acked"].(float64)
		return v >= 1
	})
	if cp.acks.Load() == nil {
		t.Fatal("跨服务命令必须回执")
	}
	// 本地命令
	code, body := adminCall(t, ts.URL, "POST", "/admin/v1/runtime/commands", "admin-token",
		`{"action":"cache_purge","params":{"all":true},"reason":"manual"}`)
	if code != 200 || !strings.Contains(body, `"acked"`) {
		t.Fatalf("本地命令应执行并回执: %d %s", code, body)
	}
	logs := waitJSON(t, ts.URL+"/admin/v1/runtime/logs", "admin-token", func(m map[string]any) bool {
		entries, _ := m["entries"].([]any)
		return len(entries) > 0
	})
	if logs["entries"] == nil {
		t.Fatal("运行时日志应可读")
	}

	// 服务只读 API：认证、脱敏、无凭据
	if code, _ := adminCall(t, ts.URL, "GET", "/service/v1/summary", "", ""); code != 401 {
		t.Fatalf("服务只读 API 匿名必须 401: %d", code)
	}
	code, body = adminCall(t, ts.URL, "GET", "/service/v1/summary", managedReadToken, "")
	if code != 200 || !strings.Contains(body, `"source":"muniu"`) {
		t.Fatalf("服务只读摘要不符: %d %s", code, body)
	}
	for _, forbidden := range []string{"service_token", "client_secret", "Authorization", "password"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("服务只读摘要不得包含 %s: %s", forbidden, body)
		}
	}

	// 审计：快照应用 + 跨服务命令
	assertSystemAudit(t, cfg)
}

func containsRoute(routes []string, want string) bool {
	for _, r := range routes {
		if r == want {
			return true
		}
	}
	return false
}

func postChat(t *testing.T, base, key string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func waitUsage(cp *controlPlaneServer, alias string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, ok := cp.usage.Load().(string); ok && strings.Contains(raw, `"source":"muniu"`) &&
			strings.Contains(raw, alias) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func adminCall(t *testing.T, base, method, path, token, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, base+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(raw)
}

func assertSystemAudit(t *testing.T, cfg *config.Config) {
	t.Helper()
	db, err := config0Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int64
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM admin_audit_log
		WHERE actor_kind='system'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("快照/命令系统审计应至少 2 条: %d", n)
	}
	var snapshotAudit, commandAudit int64
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT COUNT(*) FROM admin_audit_log WHERE action='snapshot.apply'),
		(SELECT COUNT(*) FROM admin_audit_log WHERE action LIKE 'command.%')`).
		Scan(&snapshotAudit, &commandAudit); err != nil {
		t.Fatal(err)
	}
	if snapshotAudit == 0 || commandAudit == 0 {
		t.Fatalf("快照/命令审计缺失: snapshot=%d command=%d", snapshotAudit, commandAudit)
	}
}
