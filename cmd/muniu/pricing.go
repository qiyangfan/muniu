// 动态价目合并：YAML pricing 为静态兜底，model_pricing 表为运营覆盖（管理面在线改）。
// DB 行按别名覆盖 YAML，未覆盖别名回落 YAML；任何改价后经 buildPricingDirectory
// 整体重建并原子替换目录，在途请求用旧表完成，下一请求生效。
package main

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
)

// staticPricing YAML 静态价目的运行态形态：缺省三段价、时区与倍率窗规格。
type staticPricing struct {
	def   metering.Pricing
	zone  *time.Location
	specs []metering.WindowSpec
}

// buildPricingTables 合并静态与运行态价目，装配期与管理面刷新共用；调用方包装为目录并原子替换。
func buildPricingTables(db *sql.DB, static map[string]staticPricing, settlement string, fxCnyPerUsdMilli int64) (map[string]*metering.PricingTable, error) {
	tables := make(map[string]*metering.PricingTable, len(static))
	for alias, sp := range static {
		// 本位币结算：异币种报价按汇率一次换算，热路径零变化。
		def, ok := metering.SettlePrice(sp.def, settlement, fxCnyPerUsdMilli)
		if !ok {
			return nil, fmt.Errorf("pricing[%s] 报价币种 %s 与本位币 %s 不同且未配置 fx_usd_cny_milli", alias, sp.def.Currency, settlement)
		}
		tbl, err := metering.NewPricingTable(def, sp.zone, sp.specs)
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
		tbl, err := pricingTableFromRecord(r, settlement, fxCnyPerUsdMilli)
		if err != nil {
			return nil, err
		}
		tables[r.Alias] = tbl // DB 覆盖同别名 YAML
	}
	return tables, nil
}

// pricingTableFromRecord 运行态记录转价目表；管理面写入前已过校验，此处失败即数据损坏，拒绝启动或刷新。
func pricingTableFromRecord(r store.PricingRecord, settlement string, fxCnyPerUsdMilli int64) (*metering.PricingTable, error) {
	if r.Currency != "CNY" && r.Currency != "USD" {
		return nil, fmt.Errorf("pricing[%s] currency 非法 %q", r.Alias, r.Currency)
	}
	// 按汇率将原报价换算为本位币，保证配额汇总使用同一币种。
	quote := metering.Pricing{
		Currency:               r.Currency,
		InputCreditsPerM:       r.InputCredits,
		CachedInputCreditsPerM: r.CachedInputCredits,
		VisionInputCreditsPerM: r.VisionInputCredits,
		OutputCreditsPerM:      r.OutputCredits,
	}
	def, ok := metering.SettlePrice(quote, settlement, fxCnyPerUsdMilli)
	if !ok {
		return nil, fmt.Errorf("pricing[%s] 报价币种 %s 与本位币 %s 不同且未配置 fx_usd_cny_milli", r.Alias, r.Currency, settlement)
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
		// 手工改库等带外写入可能注入越界基点，重建时与 ParseMultiplier 同域断言。
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

// formatClockMinutes 当日分钟转 "HH:MM"；1440 输出 24:00，即窗口终点语义。
func formatClockMinutes(m int) string {
	if m == 1440 {
		return "24:00"
	}
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// windowsEqual 时段窗逐条相等；顺序敏感，先声明先匹配是匹配语义的一部分。
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
