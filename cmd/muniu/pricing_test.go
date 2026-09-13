// 动态价目合并契约测试(YAML 兜底 + DB 覆盖):DB 行覆盖同别名、新别名追加、
// 撤销覆盖回落 YAML、非法 DB 行拒绝重建(脏数据不静默进热路径)。
package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
)

func TestBuildPricingTables_合并语义(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pricing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	static := map[string]staticPricing{
		"alias-a": {
			def:   metering.Pricing{Currency: "CNY", InputCreditsPerM: 2_000_000, OutputCreditsPerM: 8_000_000},
			zone:  time.UTC,
			specs: []metering.WindowSpec{{StartMin: 0, EndMin: 1440, BasisPoints: 5000}},
		},
	}

	// 1) 无 DB 行:纯 YAML 兜底(含倍率窗)
	tables, err := buildPricingTables(db, static, "CNY", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := tables["alias-a"].At(time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)); got.InputCreditsPerM != 1_000_000 {
		t.Fatalf("YAML 窗内应半价: %+v", got)
	}

	// 2) DB 覆盖同别名(0.22 缺省价,无窗):YAML 窗被整体取代
	if err := store.UpsertPricing(db, store.PricingRecord{
		Alias: "alias-a", Currency: "CNY", InputCredits: 220_000, OutputCredits: 660_000,
		Timezone: "UTC", UpdatedAt: "2026-09-11T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertPricing(db, store.PricingRecord{
		Alias: "alias-b", Currency: "CNY", InputCredits: 100, OutputCredits: 100,
		Timezone: "UTC", UpdatedAt: "2026-09-11T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	tables, err = buildPricingTables(db, static, "CNY", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := tables["alias-a"].At(time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)); got.InputCreditsPerM != 220_000 {
		t.Fatalf("DB 覆盖后应 0.22: %+v", got)
	}
	if tables["alias-b"] == nil {
		t.Fatal("DB 新别名应追加")
	}

	// 3) 撤销覆盖:删除后回落 YAML
	if _, err := store.DeletePricing(db, "alias-a"); err != nil {
		t.Fatal(err)
	}
	tables, err = buildPricingTables(db, static, "CNY", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := tables["alias-a"].At(time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)); got.InputCreditsPerM != 1_000_000 {
		t.Fatalf("撤销覆盖应回落 YAML 半价: %+v", got)
	}

	// 4) 非法 DB 行(窗口越界):重建报错,绝不静默放行脏数据
	if err := store.UpsertPricing(db, store.PricingRecord{
		Alias: "bad", Currency: "CNY", InputCredits: 1, OutputCredits: 1,
		Timezone: "UTC", Windows: []store.PricingWindowRecord{{Window: "25:00-26:00", BasisPoints: 5000}},
		UpdatedAt: "2026-09-11T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPricingTables(db, static, "CNY", 0); err == nil {
		t.Fatal("非法 DB 行必须使重建失败")
	}
}
