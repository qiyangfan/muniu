// 动态价目管理端点：GET 列表含 db 来源与 yaml 兜底来源，PUT 运行态覆盖先校验后持久化再刷新目录，
// 改价即时生效零重启，DELETE 撤销覆盖回落 YAML。
// 价目是运营态数据：厂商改价走本接口或控制台，不碰配置中心。
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

// PricingView 价目条目，GET 输出与 PUT 输入的人类可读形态；金额与倍率均为字符串，禁 float。
// 导出供控制面拉取循环复用同一校验与落库路径。
type PricingView struct {
	Alias       string              `json:"alias"`
	Source      string              `json:"source"` // db=运营覆盖 | yaml=静态兜底
	Currency    string              `json:"currency"`
	Input       string              `json:"input"`
	CachedInput string              `json:"cached_input"`
	VisionInput string              `json:"vision_input,omitempty"`
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

// pricingList GET /admin/v1/pricing：DB 运行态含窗口优先列出，YAML 兜底别名补齐。
// yaml 行仅展示缺省价，窗口细节以配置文件为准；对其 PUT 即建 DB 覆盖。
func pricingList(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListPricing(r.Context(), d.DB)
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
				Input:       metering.FormatPrice(rec.InputCredits),
				CachedInput: metering.FormatPrice(rec.CachedInputCredits),
				VisionInput: optionalPrice(rec.VisionInputCredits),
				Output:      metering.FormatPrice(rec.OutputCredits),
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
				Input:       metering.FormatPrice(entry.Def.InputCreditsPerM),
				CachedInput: metering.FormatPrice(entry.Def.CachedInputCreditsPerM),
				VisionInput: optionalPrice(entry.Def.VisionInputCreditsPerM),
				Output:      metering.FormatPrice(entry.Def.OutputCreditsPerM),
				Timezone:    entry.Timezone,
				TimeWindows: entry.TimeWindows,
			})
		}
		// 稳定输出：alias 升序；插入排序，列表极短
		for i := 1; i < len(items); i++ {
			for j := i; j > 0 && items[j].Alias < items[j-1].Alias; j-- {
				items[j], items[j-1] = items[j-1], items[j]
			}
		}
		writeJSON(w, 200, map[string]any{"pricing": items})
	}
}

// pricingPut PUT /admin/v1/pricing/{alias}：先校验再持久化再刷新目录，校验复用 config/metering 解析器，零 float。
// 三步任一失败即整单失败；刷新失败时持久化已生效，待下次启动或刷新收敛。
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
		// 校验收敛到 ParsePricingRecord，与管理面、控制面拉取同一套解析器，杜绝双实现漂移
		rec, verr := ParsePricingRecord(alias, req, time.Now())
		if verr != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", verr.Error())
			return
		}
		if err := store.UpsertPricing(r.Context(), d.DB, rec); err != nil {
			respondErr(d, w, r, err, "pricing_update_failed")
			return
		}
		if d.RefreshPricing == nil {
			// 无刷新闭包 = 目录不会更新，「成功」是假象，显式失败优于静默不生效
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
			Input: metering.FormatPrice(rec.InputCredits), CachedInput: metering.FormatPrice(rec.CachedInputCredits),
			VisionInput: optionalPrice(rec.VisionInputCredits),
			Output:      metering.FormatPrice(rec.OutputCredits), Timezone: rec.Timezone,
			TimeWindows: windowsView(rec.Windows), UpdatedAt: rec.UpdatedAt,
		}})
	}
}

// pricingDelete DELETE /admin/v1/pricing/{alias}：撤销运行态覆盖，该别名回落 YAML 静态兜底。
func pricingDelete(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		alias := r.PathValue("alias")
		n, err := store.DeletePricing(r.Context(), d.DB, alias)
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

// windowsView 持久化窗转视图，基点转人类可读倍率。
func windowsView(windows []store.PricingWindowRecord) []PricingWindowView {
	out := make([]PricingWindowView, 0, len(windows))
	for _, w := range windows {
		out = append(out, PricingWindowView{Window: w.Window, Multiplier: metering.FormatMultiplier(w.BasisPoints)})
	}
	return out
}

// validatePricingAlias 别名去空白后校验非空且不超 128 字符。
func validatePricingAlias(alias string) (string, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "", fmt.Errorf("alias 必填")
	}
	if len(alias) > 128 {
		return "", fmt.Errorf("alias 过长(≤128)")
	}
	return alias, nil
}

// parseOptionalPrice 可选价目段：空串得 0，非法报字段名错误。
func parseOptionalPrice(human, field string) (int64, error) {
	if human == "" {
		return 0, nil
	}
	v, err := metering.ParsePrice(human)
	if err != nil {
		return 0, fmt.Errorf("%s 非法(十进制 ≤9 位小数)", field)
	}
	return v, nil
}

// parsePricingWindows 人类可读时段窗转持久化形态，逐条校验窗口与倍率。
func parsePricingWindows(views []PricingWindowView) ([]store.PricingWindowRecord, error) {
	windows := make([]store.PricingWindowRecord, 0, len(views))
	for _, tw := range views {
		if _, _, err := metering.ParseWindowMinutes(tw.Window); err != nil {
			return nil, fmt.Errorf("time_windows.window 非法(HH:MM-HH:MM)")
		}
		bp, err := metering.ParseMultiplier(tw.Multiplier)
		if err != nil {
			return nil, fmt.Errorf("time_windows.multiplier 非法((0,10],≤4 位小数)")
		}
		windows = append(windows, store.PricingWindowRecord{Window: tw.Window, BasisPoints: bp})
	}
	return windows, nil
}

// parsePricingCredits 解析四段价目：input/output 必填，cached_input/vision_input 缺省为 0。
func parsePricingCredits(v PricingView) ([4]int64, error) {
	var out [4]int64
	for i, f := range [4]struct{ human, field string }{
		{v.Input, "input"},
		{v.Output, "output"},
		{v.CachedInput, "cached_input"},
		{v.VisionInput, "vision_input"},
	} {
		credits, err := parseOptionalPrice(f.human, f.field)
		if err != nil {
			return out, err
		}
		out[i] = credits
	}
	return out, nil
}

// normalizePricingCurrency 归一报价币种并校验取值域。
func normalizePricingCurrency(s string) (string, error) {
	cur := strings.ToUpper(s)
	if cur != "CNY" && cur != "USD" {
		return "", fmt.Errorf("currency 须为 CNY|USD")
	}
	return cur, nil
}

// normalizePricingTimezone 缺省 UTC，并按固定偏移格式校验。
func normalizePricingTimezone(tz string) (string, error) {
	if tz == "" {
		tz = "UTC"
	}
	if _, err := metering.ParseZoneOffset(tz); err != nil {
		return "", fmt.Errorf("timezone 须为 UTC 或 ±HH:MM")
	}
	return tz, nil
}

// ParsePricingRecord 人类可读价目视图转运行态记录，含完整校验；控制面拉取与管理面 PUT 共用。
func ParsePricingRecord(alias string, v PricingView, now time.Time) (store.PricingRecord, error) {
	alias, err := validatePricingAlias(alias)
	if err != nil {
		return store.PricingRecord{}, err
	}
	cur, err := normalizePricingCurrency(v.Currency)
	if err != nil {
		return store.PricingRecord{}, err
	}
	credits, err := parsePricingCredits(v)
	if err != nil {
		return store.PricingRecord{}, err
	}
	tz, err := normalizePricingTimezone(v.Timezone)
	if err != nil {
		return store.PricingRecord{}, err
	}
	windows, err := parsePricingWindows(v.TimeWindows)
	if err != nil {
		return store.PricingRecord{}, err
	}
	return store.PricingRecord{
		Alias: alias, Currency: cur, InputCredits: credits[0], CachedInputCredits: credits[2],
		VisionInputCredits: credits[3],
		OutputCredits:      credits[1], Timezone: tz, Windows: windows,
		UpdatedAt: now.UTC().Format(time.RFC3339),
	}, nil
}

func optionalPrice(credits int64) string {
	if credits == 0 {
		return ""
	}
	return metering.FormatPrice(credits)
}
