// 动态价目合并(动态改价契约,metering.md「动态价目」):
// YAML pricing = 静态兜底,model_pricing 表 = 运营覆盖(管理面在线改)。
// 合并语义:DB 行按别名覆盖 YAML,未覆盖别名回落 YAML;任何改价后经 buildPricingDirectory
// 整体重建并原子替换目录(在途请求用旧表完成,下一请求生效)。
package main

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
)

// staticPricing YAML 静态价目的运行态形态(缺省三段价 + 时区 + 倍率窗规格)。
type staticPricing struct {
	def   metering.Pricing
	zone  *time.Location
	specs []metering.WindowSpec
}

// buildPricingTables 合并静态与运行态价目(装配期与管理面刷新共用;调用方包装为目录并原子替换)。
func buildPricingTables(db *sql.DB, static map[string]staticPricing) (map[string]*metering.PricingTable, error) {
	tables := make(map[string]*metering.PricingTable, len(static))
	for alias, sp := range static {
		tbl, err := metering.NewPricingTable(sp.def, sp.zone, sp.specs)
		if err != nil {
			return nil, fmt.Errorf("pricing[%s]: %w", alias, err)
		}
		tables[alias] = tbl
	}
	rows, err := store.ListPricing(db)
	if err != nil {
		return nil, fmt.Errorf("读取运行态价目: %w", err)
	}
	for _, r := range rows {
		tbl, err := pricingTableFromRecord(r)
		if err != nil {
			return nil, err
		}
		tables[r.Alias] = tbl // DB 覆盖同别名 YAML
	}
	return tables, nil
}

// pricingTableFromRecord 运行态记录 → 价目表(管理面写入前已过校验,此处失败即数据损坏,拒绝启动/刷新)。
func pricingTableFromRecord(r store.PricingRecord) (*metering.PricingTable, error) {
	if r.Currency != "CNY" && r.Currency != "USD" {
		return nil, fmt.Errorf("pricing[%s] currency 非法 %q", r.Alias, r.Currency)
	}
	def := metering.Pricing{
		Currency:             r.Currency,
		InputMicroPerM:       r.InputMicro,
		CachedInputMicroPerM: r.CachedInputMicro,
		OutputMicroPerM:      r.OutputMicro,
	}
	zone := time.UTC
	if r.Timezone != "" {
		loc, err := metering.ParseZoneOffset(r.Timezone)
		if err != nil {
			return nil, fmt.Errorf("pricing[%s] timezone: %w", r.Alias, err)
		}
		zone = loc
	}
	specs := make([]metering.WindowSpec, 0, len(r.Windows))
	for _, w := range r.Windows {
		s, e, err := metering.ParseWindowMinutes(w.Window)
		if err != nil {
			return nil, fmt.Errorf("pricing[%s]: %w", r.Alias, err)
		}
		// 带外写库(手工改库)可能注入越界基点:重建时与 ParseMultiplier 同域断言
		if w.BasisPoints <= 0 || w.BasisPoints > metering.MaxScaleBp {
			return nil, fmt.Errorf("pricing[%s]: 时段窗倍率非法(bp=%d)", r.Alias, w.BasisPoints)
		}
		specs = append(specs, metering.WindowSpec{StartMin: s, EndMin: e, BasisPoints: w.BasisPoints})
	}
	tbl, err := metering.NewPricingTable(def, zone, specs)
	if err != nil {
		return nil, fmt.Errorf("pricing[%s]: %w", r.Alias, err)
	}
	return tbl, nil
}

// formatClockMinutes 当日分钟 → "HH:MM"(1440 输出 24:00,窗口终点语义)。
func formatClockMinutes(m int) string {
	if m == 1440 {
		return "24:00"
	}
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// windowsEqual 时段窗逐条相等(顺序敏感:先声明先匹配的语义组成部分)。
func windowsEqual(a, b []store.PricingWindowRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
