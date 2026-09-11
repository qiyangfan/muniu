// 动态价目管理端点(metering.md「动态价目」):GET 列表(db 来源 + yaml 兜底来源)、
// PUT 运行态覆盖(先校验后持久化再刷新目录,改价即时生效零重启)、DELETE 撤销覆盖(回落 YAML)。
// 价目是运营态数据(ADR-0004 分工边界):厂商改价走本接口/控制台,不碰配置中心。
package admin

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
)

// PricingView 价目条目(GET 输出/PUT 输入的人类可读形态;金额与倍率均为字符串,禁 float)。
// 导出供控制面拉取循环复用同一校验与落库路径(跨仓契约的同构解析)。
type PricingView struct {
	Alias       string              `json:"alias"`
	Source      string              `json:"source"` // db=运营覆盖 | yaml=静态兜底
	Currency    string              `json:"currency"`
	Input       string              `json:"input"`
	CachedInput string              `json:"cached_input"`
	Output      string              `json:"output"`
	Timezone    string              `json:"timezone"`
	TimeWindows []PricingWindowView `json:"time_windows"`
	UpdatedAt   string              `json:"updated_at,omitempty"`
}

// PricingWindowView 时段窗条目。
type PricingWindowView struct {
	Window     string `json:"window"`
	Multiplier string `json:"multiplier"`
}

// pricingList GET /admin/v1/pricing:DB 运行态(含窗口)优先列出,YAML 兜底别名补齐
// (yaml 行仅展示缺省价——窗口细节以配置文件为准;对其 PUT 即建 DB 覆盖)。
func pricingList(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListPricing(d.DB)
		if err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		inDB := map[string]bool{}
		items := make([]PricingView, 0, len(rows)+len(d.StaticPricing))
		for _, rec := range rows {
			inDB[rec.Alias] = true
			items = append(items, PricingView{
				Alias: rec.Alias, Source: "db", Currency: rec.Currency,
				Input:       metering.FormatPrice(rec.InputMicro),
				CachedInput: metering.FormatPrice(rec.CachedInputMicro),
				Output:      metering.FormatPrice(rec.OutputMicro),
				Timezone:    rec.Timezone,
				TimeWindows: windowsView(rec.Windows),
				UpdatedAt:   rec.UpdatedAt,
			})
		}
		for alias, entry := range d.StaticPricing {
			if inDB[alias] {
				continue
			}
			items = append(items, PricingView{
				Alias: alias, Source: "yaml", Currency: entry.Def.Currency,
				Input:       metering.FormatPrice(entry.Def.InputMicroPerM),
				CachedInput: metering.FormatPrice(entry.Def.CachedInputMicroPerM),
				Output:      metering.FormatPrice(entry.Def.OutputMicroPerM),
				Timezone:    entry.Timezone,
				TimeWindows: entry.TimeWindows,
			})
		}
		// 稳定输出:alias 升序(插入排序,列表极短)
		for i := 1; i < len(items); i++ {
			for j := i; j > 0 && items[j].Alias < items[j-1].Alias; j-- {
				items[j], items[j-1] = items[j-1], items[j]
			}
		}
		writeJSON(w, 200, map[string]any{"pricing": items})
	}
}

// pricingPut PUT /admin/v1/pricing/{alias}:先校验(同 config/metering 解析器,零 float)→
// 持久化 → 刷新目录。三步任一失败即整单失败;刷新失败时持久化已生效(下次启动/刷新收敛)。
func pricingPut(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		alias := strings.TrimSpace(r.PathValue("alias"))
		if alias == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "alias 必填")
			return
		}
		if len(alias) > 128 {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "alias 过长(≤128)")
			return
		}
		var req PricingView
		if err := decode(r, &req); err != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "请求体非法")
			return
		}
		req.Alias = alias
		// 校验收敛到 ParsePricingRecord(与管理面/控制面拉取同一套解析器,杜绝双实现漂移)
		rec, verr := ParsePricingRecord(alias, req, time.Now())
		if verr != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", verr.Error())
			return
		}
		if err := store.UpsertPricing(d.DB, rec); err != nil {
			respondErr(d, w, r, err, "pricing_update_failed")
			return
		}
		if d.RefreshPricing == nil {
			// 无刷新闭包 = 目录不会更新,「成功」是假象——显式失败优于静默不生效
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "价目目录刷新未装配")
			return
		}
		if err := d.RefreshPricing(); err != nil {
			d.Logger.Error("价目目录刷新失败(持久化已生效,待下次刷新收敛)",
				"request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "价目已保存但刷新失败,请重试")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "pricing": PricingView{
			Alias: rec.Alias, Source: "db", Currency: rec.Currency,
			Input: metering.FormatPrice(rec.InputMicro), CachedInput: metering.FormatPrice(rec.CachedInputMicro),
			Output: metering.FormatPrice(rec.OutputMicro), Timezone: rec.Timezone,
			TimeWindows: windowsView(rec.Windows), UpdatedAt: rec.UpdatedAt,
		}})
	}
}

// pricingDelete DELETE /admin/v1/pricing/{alias}:撤销运行态覆盖,该别名回落 YAML 静态兜底。
func pricingDelete(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		alias := r.PathValue("alias")
		n, err := store.DeletePricing(d.DB, alias)
		if err != nil {
			respondErr(d, w, r, err, "pricing_delete_failed")
			return
		}
		if n == 0 {
			writeAdminError(w, r, 404, protocol.TypeBadRequest, "pricing_not_found", "该别名无运行态覆盖")
			return
		}
		if d.RefreshPricing == nil {
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "价目目录刷新未装配")
			return
		}
		if err := d.RefreshPricing(); err != nil {
			d.Logger.Error("价目目录刷新失败(删除已生效,待下次刷新收敛)",
				"request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "价目已删除但刷新失败,请重试")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// windowsView 持久化窗 → 视图(基点 → 人类可读倍率)。
func windowsView(windows []store.PricingWindowRecord) []PricingWindowView {
	out := make([]PricingWindowView, 0, len(windows))
	for _, w := range windows {
		out = append(out, PricingWindowView{Window: w.Window, Multiplier: metering.FormatMultiplier(w.BasisPoints)})
	}
	return out
}

// ParsePricingRecord 人类可读价目视图 → 运行态记录(完整校验;控制面拉取与管理面 PUT 共用)。
func ParsePricingRecord(alias string, v PricingView, now time.Time) (store.PricingRecord, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return store.PricingRecord{}, fmt.Errorf("alias 必填")
	}
	if len(alias) > 128 {
		return store.PricingRecord{}, fmt.Errorf("alias 过长(≤128)")
	}
	cur := strings.ToUpper(v.Currency)
	if cur != "CNY" && cur != "USD" {
		return store.PricingRecord{}, fmt.Errorf("currency 须为 CNY|USD")
	}
	inMicro, err := metering.ParsePrice(v.Input)
	if err != nil {
		return store.PricingRecord{}, fmt.Errorf("input 非法(十进制 ≤6 位小数)")
	}
	outMicro, err := metering.ParsePrice(v.Output)
	if err != nil {
		return store.PricingRecord{}, fmt.Errorf("output 非法(十进制 ≤6 位小数)")
	}
	cachedMicro := int64(0)
	if v.CachedInput != "" {
		if cachedMicro, err = metering.ParsePrice(v.CachedInput); err != nil {
			return store.PricingRecord{}, fmt.Errorf("cached_input 非法(十进制 ≤6 位小数)")
		}
	}
	tz := v.Timezone
	if tz == "" {
		tz = "UTC"
	}
	if _, err := metering.ParseZoneOffset(tz); err != nil {
		return store.PricingRecord{}, fmt.Errorf("timezone 须为 UTC 或 ±HH:MM")
	}
	windows := make([]store.PricingWindowRecord, 0, len(v.TimeWindows))
	for _, tw := range v.TimeWindows {
		if _, _, err := metering.ParseWindowMinutes(tw.Window); err != nil {
			return store.PricingRecord{}, fmt.Errorf("time_windows.window 非法(HH:MM-HH:MM)")
		}
		bp, err := metering.ParseMultiplier(tw.Multiplier)
		if err != nil {
			return store.PricingRecord{}, fmt.Errorf("time_windows.multiplier 非法((0,10],≤4 位小数)")
		}
		windows = append(windows, store.PricingWindowRecord{Window: tw.Window, BasisPoints: bp})
	}
	return store.PricingRecord{
		Alias: alias, Currency: cur, InputMicro: inMicro, CachedInputMicro: cachedMicro,
		OutputMicro: outMicro, Timezone: tz, Windows: windows,
		UpdatedAt: now.UTC().Format(time.RFC3339),
	}, nil
}
