// 装配层端到端测试：配置→存储→鉴权→限流→代理→fake 上游全链，验证流式与非流式
// 透传、鉴权负面路径与 usage_logs 计量落账，覆盖 main 的 assemble 逻辑。
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

const e2eAlias = "alias-a"

const (
	e2eStreamBody = `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	e2eJSONBody   = `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`
)

// newE2E 起一个 fake 上游与指向它的完整装配，含存储/鉴权/限流/计量，返回可请求的 server、
// 已签发入库的明文 key 与断言用 db 打开函数。
func newE2E(t *testing.T, events []testkit.Event, jsonStatus int, jsonBody string) (*httptest.Server, string, *sql.DB) {
	t.Helper()
	t.Setenv("E2E_FAKE_KEY", "sk-e2e")
	var up *testkit.Upstream
	if events != nil {
		up = testkit.StartStreamUpstream(t, events)
	} else {
		up = testkit.StartJSONUpstream(t, jsonStatus, jsonBody)
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "muniu.db")
	cfgPath := filepath.Join(dir, "muniu.yaml")
	cfgContent := `
server:
  addr: ":0"
log:
  level: error
  format: text
storage:
  sqlite:
    path: "` + dbPath + `"
providers:
  - name: openai
    adapter: openai
    base_url: "` + up.URL() + `"
    keys:
      - env: E2E_FAKE_KEY
    weight: 10
model_routes:
  ` + e2eAlias + `:
    - { provider: openai, model: upstream-a, priority: 0 }
pricing:
  ` + e2eAlias + `:
    currency: CNY
    input: "2.00"
    output: "8.00"
`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("配置加载失败: %v", err)
	}

	// 先签发一把测试 key，装配会重新打开同一库。
	seed, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	kdb, err := config0Open(seed)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, _, err := auth.CreateKey(kdb, "default", "e2e", 60, auth.KeyOpts{})
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	_ = kdb.Close()

	handler, _, cleanup, err := assemble(cfg, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	t.Cleanup(cleanup)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	assertDB, err := config0Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = assertDB.Close() })
	return ts, plaintext, assertDB
}

func config0Open(cfg *config.Config) (*sql.DB, error) {
	return store.Open(cfg.Storage.SQLite.Path)
}

func TestE2E_流式_透传与计量落账(t *testing.T) {
	ts, plaintext, adb := newE2E(t, testkit.NormalEvents(), 0, "")

	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(e2eStreamBody))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 %d: %s", resp.StatusCode, body)
	}
	if !strings.HasSuffix(string(body), "data: [DONE]\n\n") {
		t.Fatalf("流式未干净收尾: %q", body)
	}
	if resp.Header.Get("X-Muniu-Request-Id") == "" {
		t.Fatal("未回带 X-Muniu-Request-Id")
	}

	rows := waitUsageLogs(t, adb, 1)
	r := rows[0]
	// testkit usage 帧 prompt 12 / completion 34，价目 2.00/8.00 元/M → 24μ+272μ=296μ，即 296000 credit。
	assertRow(t, r, map[string]any{
		"stream": float64(1), "status": "ok",
		"prompt_tokens": float64(12), "completion_tokens": float64(34),
		"cost_credits": float64(296000), "estimated": float64(0), "currency": "CNY",
		"model_alias": e2eAlias, "provider": "openai",
	})
}

func TestE2E_非流式_透传与计量落账(t *testing.T) {
	ts, plaintext, adb := newE2E(t, nil, 200, testkit.JSONOKBody)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(e2eJSONBody))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != testkit.JSONOKBody {
		t.Fatalf("非流式整包被改写: %q", body)
	}

	rows := waitUsageLogs(t, adb, 1)
	// JSONOKBody usage prompt 7 / completion 9 → 14μ+72μ=86μ。
	assertRow(t, rows[0], map[string]any{
		"stream": float64(0), "status": "ok",
		"prompt_tokens": float64(7), "completion_tokens": float64(9),
		"cost_credits": float64(86000), "estimated": float64(0),
	})
}

func TestE2E_无鉴权被拒(t *testing.T) {
	ts, _, _ := newE2E(t, nil, 200, testkit.JSONOKBody)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(e2eJSONBody))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("无鉴权应 401,实际 %d: %s", resp.StatusCode, body)
	}
	var env struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Error.Type != "auth_failed" {
		t.Fatalf("信封不符: %s", body)
	}
}

func TestE2E_健康检查(t *testing.T) {
	ts, _, _ := newE2E(t, nil, 200, "{}")
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("/healthz 响应不符: %s", body)
	}
}

// waitUsageLogs 轮询等待计量落库，管道 500ms 批量窗口异步落账。
func waitUsageLogs(t *testing.T, db *sql.DB, want int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var rows []map[string]any
		queryUsageLogs(t, db, &rows)
		if len(rows) >= want {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %d 行计量超时,实际 %d", want, len(rows))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func queryUsageLogs(t *testing.T, db *sql.DB, rows *[]map[string]any) {
	t.Helper()
	q := `SELECT stream AS stream, status AS status,
		prompt_tokens AS prompt_tokens, completion_tokens AS completion_tokens,
		cost_credits AS cost_credits, estimated AS estimated,
		IFNULL(currency,'') AS currency, IFNULL(model_alias,'') AS model_alias,
		IFNULL(provider,'') AS provider, IFNULL(request_id,'') AS request_id
		FROM usage_logs ORDER BY created_at`
	rs, err := db.Query(q)
	if err != nil {
		t.Fatalf("查计量表: %v", err)
	}
	defer func() { _ = rs.Close() }()
	cols, _ := rs.Columns()
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, c := range cols {
			row[c] = vals[i]
		}
		*rows = append(*rows, row)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
}

// assertRow 校验计量行字段；sqlite 数值经 database/sql 出来是 int64/string，统一转 float 比对。
func assertRow(t *testing.T, row map[string]any, want map[string]any) {
	t.Helper()
	for k, w := range want {
		got := row[k]
		if wf, ok := w.(float64); ok {
			gf, ok := toFloat(got)
			if !ok || gf != wf {
				t.Fatalf("字段 %s 不符: got=%v(%T) want=%v", k, got, got, w)
			}
			continue
		}
		if got != w {
			t.Fatalf("字段 %s 不符: got=%v want=%v", k, got, w)
		}
	}
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case string:
		var f float64
		_, err := fmt.Sscan(x, &f)
		return f, err == nil
	default:
		return 0, false
	}
}

// TestE2E_语义缓存命中实录：空白抖动的同义两问第二问命中 provider=semantic-cache 且 cost 0，
// 准入依赖显式 temperature=0。
func TestE2E_语义缓存命中实录(t *testing.T) {
	t.Setenv("E2E_FAKE_KEY", "sk-e2e")
	const embBody = `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.8,0.6]}],"model":"up-emb","usage":{"prompt_tokens":5,"total_tokens":5}}`
	embUp := testkit.StartJSONUpstream(t, 200, embBody)
	chatUp := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "muniu.db")
	cfgPath := filepath.Join(dir, "muniu.yaml")
	cfgContent := `
storage:
  sqlite:
    path: "` + dbPath + `"
log:
  level: error
semantic:
  enabled: true
  embedding_model: emb-alias
  dim: 2
  threshold: 0.97
  ttl_hours: 24
  capacity: 100
providers:
  - name: chat-fake
    adapter: openai
    base_url: "` + chatUp.URL() + `"
    keys: [ { env: E2E_FAKE_KEY } ]
    weight: 1
  - name: emb-fake
    adapter: openai
    base_url: "` + embUp.URL() + `"
    keys: [ { env: E2E_FAKE_KEY } ]
    weight: 1
model_routes:
  alias-a:
    - { provider: chat-fake, model: up-a, priority: 0 }
  emb-alias:
    - { provider: emb-fake, model: up-emb, priority: 0 }
pricing:
  alias-a:
    currency: CNY
    input: "2.00"
    output: "8.00"
`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// 种子 key + 打开 default 租户语义缓存开关。
	kdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, _, err := auth.CreateKey(kdb, "default", "cache-e2e", 600, auth.KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kdb.Exec(`UPDATE tenants SET semantic_cache_enabled = 1 WHERE name = 'default'`); err != nil {
		t.Fatal(err)
	}

	handler, _, cleanup, err := assemble(cfg, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	t.Cleanup(cleanup)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	post := func(content string) (int, string) {
		body := `{"model":"alias-a","temperature":0,"messages":[{"role":"user","content":"` + content + `"}]}`
		req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+plaintext)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(b)
	}

	// 第一问：未命中 → 上游。
	code1, body1 := post("法国的首都是哪里")
	if code1 != 200 || body1 != testkit.JSONOKBody {
		t.Fatalf("第一问应上游返回: %d %s", code1, body1)
	}
	// 第二问：空白抖动的同义问 → 命中。
	code2, body2 := post("法国的 首都是哪里")
	if code2 != 200 || body2 != testkit.JSONOKBody {
		t.Fatalf("第二问应命中回放: %d %s", code2, body2)
	}

	// 断言计量：上游行成本正常，命中行 provider=semantic-cache 且 cost 0。
	assertDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = assertDB.Close() })
	var rows []struct {
		Provider    string
		CostCredits int64
	}
	deadline := time.Now().Add(3 * time.Second) // 计量批量落库窗口轮询
	for {
		rows = nil
		rs, err := assertDB.Query(`SELECT provider, cost_credits FROM usage_logs ORDER BY created_at`)
		if err != nil {
			t.Fatal(err)
		}
		for rs.Next() {
			var p string
			var c int64
			if err := rs.Scan(&p, &c); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, struct {
				Provider    string
				CostCredits int64
			}{p, c})
		}
		_ = rs.Close()
		if len(rows) >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// 4 行 = 问1嵌入 emb-fake、问1 chat-fake 86μ、问2嵌入 emb-fake、问2命中 semantic-cache 0μ；
	// 嵌入调用也经过计量中间件。
	if len(rows) != 4 {
		t.Fatalf("应 4 行计量(2 嵌入 + 1 上游 + 1 命中),实际 %d: %+v", len(rows), rows)
	}
	byProvider := map[string]int{}
	for _, r := range rows {
		byProvider[r.Provider]++
	}
	if byProvider["emb-fake"] != 2 || byProvider["chat-fake"] != 1 || byProvider["semantic-cache"] != 1 {
		t.Fatalf("provider 分布不符: %+v", byProvider)
	}
	for _, r := range rows {
		if r.Provider == "semantic-cache" && (r.CostCredits != 0) {
			t.Fatalf("命中行必须 cost 0: %+v", r)
		}
		if r.Provider == "chat-fake" && r.CostCredits != 86000 {
			t.Fatalf("上游行成本不符: %+v", r)
		}
	}
}
