// store 管理侧扩展测试：租户 CRUD 与校验、usage 四维聚合，含语义缓存命中与 estimated 剔除。
package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
	"uuid"
)

// sqlDB 标准库连接别名，测试内直用。
type sqlDB = sql.DB

// adminDBHandle 便于测试内传递连接的别名。
type adminDBHandle = sqlDB

func openAdminDB(t *testing.T) *adminDBHandle {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestTenant_CRUD与校验。
func TestTenant_CRUD(t *testing.T) {
	db := openAdminDB(t)

	id, err := CreateTenant(db, "acme")
	if err != nil {
		t.Fatalf("创建租户: %v", err)
	}
	if _, err := CreateTenant(db, "acme"); err == nil {
		t.Fatal("重名租户应报错")
	}

	ok, err := UpdateTenant(db, id, UpdateTenantPatch{Status: "disabled"})
	if err != nil || !ok {
		t.Fatalf("更新租户: %v %v", ok, err)
	}
	if ok, err = UpdateTenant(db, "no-such", UpdateTenantPatch{Status: "active"}); err != nil || ok {
		t.Fatalf("不存在租户应 ok=false: %v %v", ok, err)
	}
	if _, err = UpdateTenant(db, id, UpdateTenantPatch{Status: "weird"}); err == nil {
		t.Fatal("非法 status 应报错")
	}

	tenants, err := ListTenants(db)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tn := range tenants {
		if tn.ID == id {
			found = true
			if tn.Status != "disabled" {
				t.Fatalf("状态未生效: %+v", tn)
			}
		}
	}
	if !found {
		t.Fatal("列表未含新租户")
	}
	// default 种子恒在。
	hasDefault := false
	for _, tn := range tenants {
		if tn.Name == "default" {
			hasDefault = true
		}
	}
	if !hasDefault {
		t.Fatal("default 租户缺失")
	}
}

func TestTenant_语义缓存开关(t *testing.T) {
	db := openAdminDB(t)
	id, _ := CreateTenant(db, "cache-on")
	en := true
	if ok, err := UpdateTenant(db, id, UpdateTenantPatch{SemanticCacheEnable: &en}); err != nil || !ok {
		t.Fatalf("开缓存开关: %v %v", ok, err)
	}
	tenants, _ := ListTenants(db)
	for _, tn := range tenants {
		if tn.ID == id && !tn.SemanticCacheEnable {
			t.Fatal("开关未生效")
		}
	}
}

func TestUsageAggregate_四维与口径(t *testing.T) {
	db := openAdminDB(t)
	now := time.Now().UTC()

	// 三行：普通成功、语义缓存命中、estimated 行，其中 estimated 行不计成本。
	rows := []UsageRow{
		{ID: uuid.NewV7().String(), RequestID: "r1", TenantID: "t1", APIKeyID: "k1",
			Provider: "deepseek", ModelAlias: "m1", UpstreamModel: "u", Status: "ok",
			PromptTokens: 100, CompletionTokens: 50, CostCredits: 500, Currency: "CNY",
			LatencyTotalMs: 1, CreatedAt: now},
		{ID: uuid.NewV7().String(), RequestID: "r2", TenantID: "t1", APIKeyID: "k1",
			Provider: "semantic-cache", ModelAlias: "m1", UpstreamModel: "u", Status: "ok",
			PromptTokens: 100, CompletionTokens: 50, CostCredits: 0, Currency: "CNY",
			LatencyTotalMs: 1, CreatedAt: now},
		{ID: uuid.NewV7().String(), RequestID: "r3", TenantID: "t1", APIKeyID: "k2",
			Provider: "deepseek", ModelAlias: "m2", UpstreamModel: "u", Status: "error",
			PromptTokens: 10, CompletionTokens: 0, Estimated: true, CostCredits: 999, Currency: "CNY",
			LatencyTotalMs: 1, CreatedAt: now},
	}
	if err := InsertUsageBatch(db, rows); err != nil {
		t.Fatal(err)
	}
	from := now.Add(-time.Minute).Format(time.RFC3339)
	to := now.Add(time.Minute).Format(time.RFC3339)

	// 按 provider 聚合：deepseek 成本只计 500，estimated 999 剔除；semantic-cache 命中 1 次。
	pts, err := UsageAggregate(db, DimProvider, from, to, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("应 2 个 provider 桶: %+v", pts)
	}
	byProv := map[string]UsagePoint{}
	for _, p := range pts {
		byProv[p.Dim] = p
	}
	ds := byProv["deepseek"]
	if ds.Requests != 2 || ds.CostCredits != 500 {
		t.Fatalf("deepseek 聚合不符(estimated 须剔除): %+v", ds)
	}
	sc := byProv["semantic-cache"]
	if sc.CacheHits != 1 || sc.CostCredits != 0 {
		t.Fatalf("语义缓存命中口径不符: %+v", sc)
	}

	// 按 day
	days, err := UsageAggregate(db, DimDay, from, to, "", "")
	if err != nil || len(days) != 1 {
		t.Fatalf("按日聚合不符: %v %v", days, err)
	}
	if days[0].Requests != 3 || days[0].CostCredits != 500 {
		t.Fatalf("按日口径不符: %+v", days[0])
	}

	// 按 key + tenant 过滤
	keys, err := UsageAggregate(db, DimKey, from, to, "t1", "k2")
	if err != nil || len(keys) != 1 || keys[0].Dim != "k2" {
		t.Fatalf("按 key 过滤不符: %v %v", keys, err)
	}
}
