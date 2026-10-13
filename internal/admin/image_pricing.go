package admin

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
)

// ImagePricingView 报价以十进制字符串展示，不暴露汇率换算后的内部价格。
type ImagePricingView struct {
	Alias     string            `json:"alias"`
	Source    string            `json:"source,omitempty"`
	Currency  string            `json:"currency"`
	Default   string            `json:"default"`
	Tiers     map[string]string `json:"tiers"`
	UpdatedAt string            `json:"updated_at,omitempty"`
}

func imagePricingView(r store.ImagePricingRecord, source string) ImagePricingView {
	tiers := map[string]string{}
	for k, v := range r.Tiers {
		tiers[k] = metering.FormatPrice(v)
	}
	return ImagePricingView{Alias: r.Alias, Source: source, Currency: r.Currency, Default: metering.FormatPrice(r.DefaultCredits), Tiers: tiers, UpdatedAt: r.UpdatedAt}
}

func imagePricingList(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListImagePricing(d.DB)
		if err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		items := map[string]ImagePricingView{}
		for alias, p := range d.StaticImagePricing {
			items[alias] = imagePricingView(store.ImagePricingRecord{Alias: alias, Currency: p.Currency, DefaultCredits: p.DefaultCredits, Tiers: p.Tiers}, "yaml")
		}
		for _, rec := range rows {
			items[rec.Alias] = imagePricingView(rec, "db")
		}
		out := make([]ImagePricingView, 0, len(items))
		for _, v := range items {
			out = append(out, v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
		writeJSON(w, 200, map[string]any{"pricing": out})
	}
}

func imagePricingPut(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var v ImagePricingView
		if err := decode(r, &v); err != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "价目请求非法")
			return
		}
		alias := strings.TrimSpace(r.PathValue("alias"))
		price, err := metering.ParseImagePrice(v.Currency, v.Default, v.Tiers)
		if err != nil || alias == "" || len(alias) > 128 {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "别名、币种、兜底价或档位非法")
			return
		}
		rec := store.ImagePricingRecord{Alias: alias, Currency: price.Currency, DefaultCredits: price.DefaultCredits, Tiers: price.Tiers, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
		if d.UpdateImagePricing == nil {
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "价目目录未装配")
			return
		}
		if err := d.UpdateImagePricing(alias, &rec); err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

func imagePricingDelete(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.UpdateImagePricing == nil {
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "价目目录未装配")
			return
		}
		err := d.UpdateImagePricing(r.PathValue("alias"), nil)
		if errors.Is(err, store.ErrNotFound) {
			writeAdminError(w, r, 404, protocol.TypeNotFound, "pricing_not_found", "没有该运行态价目")
			return
		}
		if err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}
