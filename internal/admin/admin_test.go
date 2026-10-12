// 管理面契约测试:token 关闭/鉴权失败、租户与 key CRUD、usage 聚合参数校验、providers 拓扑、缓存统计空态。
package admin_test

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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"

	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newAdmin(t *testing.T, db *sql.DB, token string, pools map[string]*keypool.Pool) http.Handler {
	t.Helper()
	h := admin.New(admin.Deps{
		DB:        db,
		Token:     token,
		Providers: []config.Provider{{Name: "p1", Adapter: "openai", BaseURL: "https://x/v1", Keys: []config.KeyRef{{Env: "K1", Weight: 5}}}},
		Pools:     pools,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if h == nil {
		t.Fatal("token 非空时 Handler 不应为 nil")
	}
	return h
}

func call(t *testing.T, h http.Handler, method, path, token, body string) (int, string) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

func TestAdmin_token缺失整体关闭(t *testing.T) {
	db := openDB(t)
	if h := admin.New(admin.Deps{DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}); h != nil {
		t.Fatal("token 缺失必须返回 nil(管理面关闭)")
	}
}

func TestAdmin_鉴权失败401(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "right-token", nil)
	code, body := call(t, h, "GET", "/admin/v1/tenants", "wrong-token", "")
	if code != 401 || !strings.Contains(body, "auth_failed") {
		t.Fatalf("鉴权失败应 401: %d %s", code, body)
	}
}

func TestAdmin_租户CRUD全链(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil)

	// 创建
	code, body := call(t, h, "POST", "/admin/v1/tenants", "tok", `{"name":"acme"}`)
	if code != 200 {
		t.Fatalf("创建失败: %d %s", code, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &created)

	// 列表含 default + acme
	code, body = call(t, h, "GET", "/admin/v1/tenants", "tok", "")
	if code != 200 || !strings.Contains(body, "acme") || !strings.Contains(body, "default") {
		t.Fatalf("列表不符: %d %s", code, body)
	}

	// 更新:禁用 + 配额
	quota := int64(5000)
	patch, _ := json.Marshal(map[string]any{"status": "disabled", "daily_quota_credits": quota})
	code, body = call(t, h, "PATCH", "/admin/v1/tenants/"+created.ID, "tok", string(patch))
	if code != 200 {
		t.Fatalf("更新失败: %d %s", code, body)
	}
	code, _ = call(t, h, "PATCH", "/admin/v1/tenants/no-such", "tok", string(patch))
	if code != 404 {
		t.Fatalf("不存在租户应 404: %d", code)
	}

	// 状态校验
	code, _ = call(t, h, "PATCH", "/admin/v1/tenants/"+created.ID, "tok", `{"status":"weird"}`)
	if code != 400 {
		t.Fatalf("非法 status 应 400: %d", code)
	}
}

func TestAdmin_keysCRUD全链(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil)

	code, body := call(t, h, "POST", "/admin/v1/keys", "tok", `{"tenant":"default","name":"api-key","rpm_limit":120}`)
	if code != 200 {
		t.Fatalf("签发失败: %d %s", code, body)
	}
	var created struct {
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
	}
	_ = json.Unmarshal([]byte(body), &created)
	if !strings.HasPrefix(created.Key, "mk-") {
		t.Fatalf("明文形态不符: %q", created.Key)
	}

	code, body = call(t, h, "GET", "/admin/v1/keys", "tok", "")
	if code != 200 || !strings.Contains(body, created.Prefix) || strings.Contains(body, created.Key) {
		t.Fatalf("列表不得泄露明文且应含 prefix: %s", body)
	}

	code, body = call(t, h, "DELETE", "/admin/v1/keys/"+created.Prefix, "tok", "")
	if code != 200 {
		t.Fatalf("吊销失败: %d %s", code, body)
	}
	code, _ = call(t, h, "DELETE", "/admin/v1/keys/"+created.Prefix, "tok", "")
	if code != 404 {
		t.Fatalf("重复吊销应 404: %d", code)
	}
}

func TestAdmin_usage参数校验与聚合(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil)

	code, body := call(t, h, "GET", "/admin/v1/usage?group_by=bogus", "tok", "")
	if code != 400 {
		t.Fatalf("非法 group_by 应 400: %d %s", code, body)
	}
	code, body = call(t, h, "GET", "/admin/v1/usage?group_by=day", "tok", "")
	if code != 200 {
		t.Fatalf("聚合失败: %d %s", code, body)
	}
	// 空数据返回空数组而非 null
	if strings.Contains(body, "\"points\":null") {
		t.Fatalf("空数据应为空数组: %s", body)
	}
}

func TestAdmin_providers拓扑与池状态(t *testing.T) {
	db := openDB(t)
	pool := keypool.NewPool([]keypool.KeyInput{{ID: "K1", Value: "v", Weight: 5}})
	h := newAdmin(t, db, "tok", map[string]*keypool.Pool{"p1": pool})

	code, body := call(t, h, "GET", "/admin/v1/providers", "tok", "")
	if code != 200 {
		t.Fatal(body)
	}
	if !strings.Contains(body, `"state":"healthy"`) || !strings.Contains(body, `"weight":5`) {
		t.Fatalf("拓扑/池状态不符: %s", body)
	}
}

func TestAdmin_缓存统计空态(t *testing.T) {
	db := openDB(t)
	h := admin.New(admin.Deps{
		DB: db, Token: "tok", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	code, body := call(t, h, "GET", "/admin/v1/cache/stats", "tok", "")
	if code != 200 || !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("未接缓存统计应返回空态: %d %s", code, body)
	}
}

// 吊销 key 必须即时清空进程内验证缓存(Deps.Verifier 接线):未清前已缓存的 key 在 TTL 内仍可放行。
func TestAdmin_吊销key即时失效验证缓存(t *testing.T) {
	db := openDB(t)
	plaintext, rec, err := auth.CreateKey(db, "default", "revoke-pin", 60, auth.KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	verifier := auth.NewVerifier(db, auth.Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := admin.New(admin.Deps{
		DB: db, Token: "t",
		Providers: []config.Provider{{Name: "p1", Adapter: "openai", BaseURL: "https://x/v1", Keys: []config.KeyRef{{Env: "K1", Weight: 5}}}},
		Verifier:  verifier,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	probe := func() int {
		pr := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })).ServeHTTP(pr, req)
		return pr.Code
	}
	if probe() != 200 {
		t.Fatal("吊销前鉴权应通过(并预热缓存)")
	}
	code, _ := call(t, h, "DELETE", "/admin/v1/keys/"+rec.Prefix, "t", "")
	if code != 200 {
		t.Fatalf("吊销应 200: %d", code)
	}
	if got := probe(); got != 401 {
		t.Fatalf("吊销后必须即时 401(缓存已清), 实际 %d", got)
	}
}

// 动态改价全流程(动态价目契约,metering.md):GET 展示 yaml 兜底 → PUT 运行态覆盖
// (持久化+目录刷新即时生效)→ DELETE 撤销覆盖回落 yaml;校验失败整单拒绝。
func TestAdmin_动态改价全流程(t *testing.T) {
	db := openDB(t)
	staticDefs := map[string]metering.Pricing{
		"alias-yaml": {Currency: "USD", InputCreditsPerM: 440_000_000, OutputCreditsPerM: 1_320_000_000},
	}
	// 复刻装配层合并:静态兜底 + DB 覆盖 → 目录整体替换
	rebuild := func() *metering.PricingDirectory {
		tables := map[string]*metering.PricingTable{}
		for alias, def := range staticDefs {
			tbl, err := metering.NewPricingTable(def, time.UTC, nil)
			if err != nil {
				t.Fatal(err)
			}
			tables[alias] = tbl
		}
		rows, err := store.ListPricing(db)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			tz := time.UTC
			if r.Timezone == "+08:00" {
				tz = time.FixedZone("cst", 8*3600)
			}
			var wins []metering.WindowSpec
			for _, w := range r.Windows {
				s, e, err := metering.ParseWindowMinutes(w.Window)
				if err != nil {
					t.Fatal(err)
				}
				wins = append(wins, metering.WindowSpec{StartMin: s, EndMin: e, BasisPoints: w.BasisPoints})
			}
			tbl, err := metering.NewPricingTable(metering.Pricing{
				Currency: r.Currency, InputCreditsPerM: r.InputCredits,
				CachedInputCreditsPerM: r.CachedInputCredits, OutputCreditsPerM: r.OutputCredits,
			}, tz, wins)
			if err != nil {
				t.Fatal(err)
			}
			tables[r.Alias] = tbl
		}
		return metering.NewPricingDirectory(tables)
	}
	dir := rebuild()
	h := admin.New(admin.Deps{
		DB: db, Token: "t",
		Providers:     []config.Provider{{Name: "p1", Adapter: "openai", BaseURL: "https://x/v1", Keys: []config.KeyRef{{Env: "K1", Weight: 5}}}},
		PricingDir:    dir,
		StaticPricing: buildStaticEntries(staticDefs),
		RefreshPricing: func() error {
			*dir = *rebuild()
			return nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	// 1) PUT 前:GET 显示 yaml 兜底来源,目录为静态价
	code, body := call(t, h, "GET", "/admin/v1/pricing", "t", "")
	if code != 200 || !strings.Contains(body, `"source":"yaml"`) || !strings.Contains(body, `"input":"0.44"`) {
		t.Fatalf("GET 应列出 yaml 兜底: %d %s", code, body)
	}
	if got := dir.Get("alias-yaml").At(time.Now()); got.InputCreditsPerM != 440_000_000 {
		t.Fatalf("改价前应为静态价: %+v", got)
	}

	// 2) PUT 运行态覆盖(半价 + 时段窗):持久化 + 刷新,目录即时生效
	putBody := `{"currency":"USD","input":"0.22","output":"0.66","timezone":"+08:00","time_windows":[{"window":"00:30-08:30","multiplier":"0.5"}]}`
	code, body = call(t, h, "PUT", "/admin/v1/pricing/alias-yaml", "t", putBody)
	if code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("PUT 应成功: %d %s", code, body)
	}
	code, body = call(t, h, "GET", "/admin/v1/pricing", "t", "")
	if code != 200 || !strings.Contains(body, `"source":"db"`) || !strings.Contains(body, `"input":"0.22"`) ||
		!strings.Contains(body, `"multiplier":"0.5"`) {
		t.Fatalf("PUT 后 GET 应显示 db 来源与新价: %d %s", code, body)
	}
	// 目录已刷新:PUT 的 input/output 为缺省(峰时)价,窗口按倍率缩放
	// 窗内(北京 04:00)= 0.22×0.5 = 0.11(110_000_000 credit);窗外(北京 12:00)= 缺省 0.22
	cst := time.FixedZone("cst", 8*3600)
	if got := dir.Get("alias-yaml").At(time.Date(2026, 9, 11, 4, 0, 0, 0, cst)); got.InputCreditsPerM != 110_000_000 {
		t.Fatalf("刷新后窗内应 0.22×0.5=0.11: %+v", got)
	}
	if got := dir.Get("alias-yaml").At(time.Date(2026, 9, 11, 12, 0, 0, 0, cst)); got.InputCreditsPerM != 220_000_000 {
		t.Fatalf("窗外应为 DB 缺省价 0.22: %+v", got)
	}

	// 3) 校验失败整单拒绝(不落库不刷新)
	code, _ = call(t, h, "PUT", "/admin/v1/pricing/alias-yaml", "t",
		`{"currency":"USD","input":"0.22","output":"0.66","time_windows":[{"window":"00:00-24:00","multiplier":"0"}]}`)
	if code != 400 {
		t.Fatalf("非法 multiplier 应 400: %d", code)
	}
	if got := dir.Get("alias-yaml").At(time.Date(2026, 9, 11, 4, 0, 0, 0, cst)); got.InputCreditsPerM != 110_000_000 {
		t.Fatalf("拒绝后目录不应变化(窗内仍 0.11): %+v", got)
	}

	// 4) DELETE 撤销覆盖:回落 yaml 静态价;重复删除 404
	code, _ = call(t, h, "DELETE", "/admin/v1/pricing/alias-yaml", "t", "")
	if code != 200 {
		t.Fatalf("DELETE 应成功: %d", code)
	}
	if got := dir.Get("alias-yaml").At(time.Now()); got.InputCreditsPerM != 440_000_000 {
		t.Fatalf("删除后应回落 yaml 静态价: %+v", got)
	}
	code, _ = call(t, h, "DELETE", "/admin/v1/pricing/alias-yaml", "t", "")
	if code != 404 {
		t.Fatalf("重复 DELETE 应 404: %d", code)
	}
}

// buildStaticEntries 测试辅助:静态价目 → 管理面展示形态(与装配层同构)。
func buildStaticEntries(defs map[string]metering.Pricing) map[string]admin.StaticEntry {
	out := make(map[string]admin.StaticEntry, len(defs))
	for alias, def := range defs {
		out[alias] = admin.StaticEntry{Def: def, Timezone: "UTC", TimeWindows: []admin.PricingWindowView{}}
	}
	return out
}

// ---- 插件管理端点(ADR-0011 任务 5.1)----

func newAdminWithPlugins(t *testing.T, db *sql.DB, reg *plugin.Registry) http.Handler {
	t.Helper()
	h := admin.New(admin.Deps{
		DB:        db,
		Token:     "tok",
		Providers: []config.Provider{{Name: "p1", Adapter: "openai", BaseURL: "https://x/v1", Keys: []config.KeyRef{{Env: "K1", Weight: 5}}}},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Plugins:   reg,
	})
	if h == nil {
		t.Fatal("Handler 不应为 nil")
	}
	return h
}

func writeFixtureWasm(t *testing.T, kind testkit.PluginFixtureKind) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "plugin.wasm")
	if err := os.WriteFile(p, testkit.FixturePlugin(kind), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAdmin_Plugins_注册列表启停替换(t *testing.T) {
	db := openDB(t)
	reg := plugin.NewRegistry()
	t.Cleanup(reg.Close)
	h := newAdminWithPlugins(t, db, reg)

	// 注册(经文件路径)
	wasmPath := writeFixtureWasm(t, testkit.FixturePass)
	status, body := call(t, h, "PUT", "/admin/v1/plugins/masker", "tok",
		`{"path":"`+wasmPath+`","fail_open":false,"timeout_ms":50,"config":{"k":"v"}}`)
	if status != 200 {
		t.Fatalf("注册应 200: %d %s", status, body)
	}
	var put struct {
		SHA256   string `json:"sha256"`
		Replaced bool   `json:"replaced"`
		Enabled  bool   `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(body), &put); err != nil {
		t.Fatal(err)
	}
	if put.Replaced || !put.Enabled || len(put.SHA256) != 64 {
		t.Fatalf("注册回执不符: %s", body)
	}

	// 列表
	status, body = call(t, h, "GET", "/admin/v1/plugins", "tok", "")
	if status != 200 || !strings.Contains(body, `"name":"masker"`) {
		t.Fatalf("列表应含 masker: %d %s", status, body)
	}

	// 启停
	status, body = call(t, h, "PATCH", "/admin/v1/plugins/masker", "tok", `{"enabled":false}`)
	if status != 200 || !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("停用应 200: %d %s", status, body)
	}
	if reg.Enabled("masker") {
		t.Fatal("停用后注册表应反映 false")
	}

	// 版本替换:哈希变化 + replaced=true;启停状态沿用(false)
	wasmPath2 := writeFixtureWasm(t, testkit.FixtureModifyEcho)
	status, body = call(t, h, "PUT", "/admin/v1/plugins/masker", "tok", `{"path":"`+wasmPath2+`"}`)
	if status != 200 {
		t.Fatalf("替换应 200: %d %s", status, body)
	}
	var put2 struct {
		SHA256   string `json:"sha256"`
		Replaced bool   `json:"replaced"`
		Enabled  bool   `json:"enabled"`
	}
	_ = json.Unmarshal([]byte(body), &put2)
	if !put2.Replaced || put2.SHA256 == put.SHA256 {
		t.Fatalf("替换回执不符: %s", body)
	}
	if reg.Enabled("masker") {
		t.Fatal("版本替换不得改变启停状态(替换前为 false)")
	}

	// 未知插件 PATCH → 404
	status, _ = call(t, h, "PATCH", "/admin/v1/plugins/ghost", "tok", `{"enabled":true}`)
	if status != 404 {
		t.Fatalf("未知插件应 404,实际 %d", status)
	}
}

func TestAdmin_Plugins_拒载与文件缺失(t *testing.T) {
	db := openDB(t)
	reg := plugin.NewRegistry()
	t.Cleanup(reg.Close)
	h := newAdminWithPlugins(t, db, reg)

	// 非 wasm 文件拒载 400,注册表不受影响
	bad := filepath.Join(t.TempDir(), "bad.wasm")
	_ = os.WriteFile(bad, []byte("not wasm"), 0o600)
	status, body := call(t, h, "PUT", "/admin/v1/plugins/bad", "tok", `{"path":"`+bad+`"}`)
	if status != 400 || !strings.Contains(body, "plugin_rejected") {
		t.Fatalf("非 wasm 应 400 拒载: %d %s", status, body)
	}

	// 路径不存在 → 400
	status, _ = call(t, h, "PUT", "/admin/v1/plugins/x", "tok", `{"path":"/nonexistent/p.wasm"}`)
	if status != 400 {
		t.Fatalf("文件缺失应 400,实际 %d", status)
	}

	// ABI 不匹配拒载
	bin := writeFixtureWasm(t, testkit.FixtureAbiMismatch)
	status, body = call(t, h, "PUT", "/admin/v1/plugins/abi", "tok", `{"path":"`+bin+`"}`)
	if status != 400 || !strings.Contains(body, "ABI") {
		t.Fatalf("ABI 不匹配应 400: %d %s", status, body)
	}

	// 拒载风暴后注册表仍可用:注册合法插件
	good := writeFixtureWasm(t, testkit.FixturePass)
	status, _ = call(t, h, "PUT", "/admin/v1/plugins/good", "tok", `{"path":"`+good+`"}`)
	if status != 200 {
		t.Fatalf("拒载后注册合法插件应成功,实际 %d", status)
	}
}

func TestAdmin_Plugins_未启用运行时(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil) // Deps.Plugins == nil
	status, body := call(t, h, "PUT", "/admin/v1/plugins/x", "tok", `{"path":"/tmp/a"}`)
	if status != 400 || !strings.Contains(body, "plugins_disabled") {
		t.Fatalf("未启用运行时应 400 plugins_disabled: %d %s", status, body)
	}
	status, _ = call(t, h, "PATCH", "/admin/v1/plugins/x", "tok", `{"enabled":true}`)
	if status != 400 || !strings.Contains(body, "plugins_disabled") {
		t.Fatalf("未启用运行时 PATCH 应 400 plugins_disabled: %d %s", status, body)
	}
}

func TestAdmin_Plugins_资源参数钳制(t *testing.T) {
	db := openDB(t)
	reg := plugin.NewRegistry()
	t.Cleanup(reg.Close)
	h := newAdminWithPlugins(t, db, reg)
	wasmPath := writeFixtureWasm(t, testkit.FixturePass)
	for name, body := range map[string]string{
		"负超时":  `{"path":"` + wasmPath + `","timeout_ms":-1}`,
		"超时超限": `{"path":"` + wasmPath + `","timeout_ms":20000}`,
		"负内存":  `{"path":"` + wasmPath + `","memory_limit_mib":-64}`,
		"内存超限": `{"path":"` + wasmPath + `","memory_limit_mib":512}`,
		"池超限":  `{"path":"` + wasmPath + `","pool_size":1000}`,
	} {
		status, _ := call(t, h, "PUT", "/admin/v1/plugins/x", "tok", body)
		if status != 400 {
			t.Fatalf("%s 应 400 拒绝,实际 %d", name, status)
		}
	}
	if _, ok := reg.SHA256("x"); ok {
		t.Fatal("被拒参数不得留下注册记录")
	}
}

// TestAdmin_签发响应与OpenAPI契约一致(openspec fix-api-key-binding-identifiers):
// handler 实际输出的字段集合 ≡ contracts/openapi.yaml KeyCreateResponse 声明的属性集合,
// 双向钉死——运行时加字段不改契约、或契约声明运行时没有的字段,都在此处先红。
// 语义锚点:id 是用量归账/锦官绑定的连接值,不得与 secret(key)/prefix 混同;
// 创建响应的租户字段沿用既有名 tenant(值为租户名;列表 KeyRecord.tenant_id 为租户 UUID,
// 同指所属租户但值域不同——契约不重命名,描述如实区分)。
func TestAdmin_签发响应与OpenAPI契约一致(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil)
	code, body := call(t, h, "POST", "/admin/v1/keys", "tok", `{"tenant":"default","name":"contract","rpm_limit":60}`)
	if code != 200 {
		t.Fatalf("签发失败: %d %s", code, body)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("签发响应非法 JSON: %v", err)
	}
	if len(resp) == 0 {
		t.Fatal("签发响应为空")
	}
	actual := make(map[string]bool, len(resp))
	for k := range resp {
		actual[k] = true
	}

	// 语义锚点:三值互不相同,id 非空、明文带 mk- 前缀、租户字段名为 tenant
	if id, _ := resp["id"].(string); id == "" || id == resp["key"] || id == resp["prefix"] {
		t.Fatalf("id 必须是非空且区别于 secret/prefix 的独立值: %q", resp["id"])
	}
	if key, _ := resp["key"].(string); !strings.HasPrefix(key, "mk-") {
		t.Fatalf("明文形态不符: %q", resp["key"])
	}
	if _, hasTenant := resp["tenant"]; !hasTenant {
		t.Fatalf("创建响应租户字段必须保持既有名 tenant(不重命名): %v", actual)
	}
	if _, renamed := resp["tenant_id"]; renamed {
		t.Fatalf("创建响应不得出现 tenant_id(契约不做同义改名): %v", actual)
	}

	// 契约侧:解析 openapi.yaml 的 KeyCreateResponse 属性集合
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "openapi.yaml"))
	if err != nil {
		t.Fatalf("读取 openapi.yaml: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string       `yaml:"required"`
				Properties map[string]any `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.yaml 非法: %v", err)
	}
	schema, ok := doc.Components.Schemas["KeyCreateResponse"]
	if !ok {
		t.Fatal("openapi.yaml 缺 KeyCreateResponse schema(签发响应契约未声明)")
	}
	if len(schema.Properties) == 0 {
		t.Fatal("KeyCreateResponse 未声明任何属性,疑似解析失败")
	}
	declared := make(map[string]bool, len(schema.Properties))
	for k := range schema.Properties {
		declared[k] = true
	}
	// required 面:handler 无条件输出全部 8 键,契约必须同等声明「属性全集即 required 全集」;
	// 漏声明会把必返字段洗成可选,生成类型随之退化为 undefined(门禁与「双向钉死」声称对齐)
	if len(schema.Required) != len(schema.Properties) {
		t.Fatalf("KeyCreateResponse 的 required(%d 项)必须覆盖全部属性(%d 项): %v",
			len(schema.Required), len(schema.Properties), schema.Required)
	}
	for _, k := range schema.Required {
		if !declared[k] {
			t.Fatalf("required 引用了未声明属性: %q", k)
		}
	}
	// expires_at 可空语义:handler 侧 *time.Time(nil→null),契约必须 nullable(漏声明会误伤 null 消费方)
	if exp, ok := schema.Properties["expires_at"].(map[string]any); ok {
		if nullable, _ := exp["nullable"].(bool); !nullable {
			t.Fatal("expires_at 必须声明 nullable:true(运行时永不过期返回 null)")
		}
	} else {
		t.Fatal("expires_at 属性形态异常,无法校验 nullable")
	}

	var missing, extra []string
	for k := range declared {
		if !actual[k] {
			missing = append(missing, k)
		}
	}
	for k := range actual {
		if !declared[k] {
			extra = append(extra, k)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("签发响应与契约漂移:\n  契约声明而运行时未返回: %v\n  运行时返回而契约未声明: %v\n(修复:同步 contracts/openapi.yaml 与 admin.createKey)",
			sortedQuote(missing), sortedQuote(extra))
	}
}

// sortedQuote 稳定排序输出(契约漂移断言的可读差异列表)。
func sortedQuote(list []string) string {
	out := make([]string, 0, len(list))
	out = append(out, list...)
	sort.Strings(out)
	quoted := make([]string, 0, len(out))
	for _, v := range out {
		quoted = append(quoted, fmt.Sprintf("%q", v))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// TestAdmin_签发过期时刻校验 创建拒过去时刻(「签发即死」无合法诉求,静默接受会产出
// 看似正常实则已过期的 key);PATCH 置过去时刻 = 立即失效的运维语义,允许。
func TestAdmin_签发过期时刻校验(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil)

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	code, body := call(t, h, "POST", "/admin/v1/keys", "tok",
		`{"tenant":"default","name":"dead-on-arrival","expires_at":"`+past+`"}`)
	if code != 400 || !strings.Contains(body, "expires_at") {
		t.Fatalf("创建过去时刻应 400: %d %s", code, body)
	}

	// 对照:未来时刻正常签发
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	code, body = call(t, h, "POST", "/admin/v1/keys", "tok",
		`{"tenant":"default","name":"living","expires_at":"`+future+`"}`)
	if code != 200 {
		t.Fatalf("未来时刻应签发成功: %d %s", code, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &created)

	code, _ = call(t, h, "PATCH", "/admin/v1/keys/"+created.ID, "tok",
		`{"expires_at":"`+past+`"}`)
	if code != 200 {
		t.Fatalf("PATCH 置过去时刻(立即失效)应允许: %d", code)
	}
}
