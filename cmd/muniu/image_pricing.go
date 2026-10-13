package main

import (
	"database/sql"
	"sync"

	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
)

func imagePrices(rows map[string]store.ImagePricingRecord, settlement string, fx int64) (map[string]metering.ImagePrice, error) {
	out := map[string]metering.ImagePrice{}
	for alias, r := range rows {
		p, err := metering.SettleImagePrice(metering.ImagePrice{Currency: r.Currency, DefaultCredits: r.DefaultCredits, Tiers: r.Tiers}, settlement, fx)
		if err != nil {
			return nil, err
		}
		out[alias] = p
	}
	return out, nil
}

func assembleImagePricing(db *sql.DB, cfg *config.Config, settlement string, fx int64) (*metering.ImagePriceDirectory, map[string]metering.ImagePrice, func(string, *store.ImagePricingRecord) error, error) {
	static := map[string]metering.ImagePrice{}
	for alias, v := range cfg.ImagePricing {
		p, err := metering.ParseImagePrice(v.Currency, v.Default, v.Tiers)
		if err != nil {
			return nil, nil, nil, err
		}
		static[alias] = p
	}
	merge := func(alias string, change *store.ImagePricingRecord, apply bool) (map[string]store.ImagePricingRecord, error) {
		all := map[string]store.ImagePricingRecord{}
		for a, p := range static {
			all[a] = store.ImagePricingRecord{Alias: a, Currency: p.Currency, DefaultCredits: p.DefaultCredits, Tiers: p.Tiers}
		}
		rows, err := store.ListImagePricing(db)
		if err != nil {
			return nil, err
		}
		found := false
		for _, r := range rows {
			if apply && r.Alias == alias {
				found = true
				continue
			}
			all[r.Alias] = r
		}
		if apply {
			if change == nil && !found {
				return nil, store.ErrNotFound
			}
			if change != nil {
				all[alias] = *change
			}
		}
		return all, nil
	}
	all, err := merge("", nil, false)
	if err != nil {
		return nil, nil, nil, err
	}
	prices, err := imagePrices(all, settlement, fx)
	if err != nil {
		return nil, nil, nil, err
	}
	dir := metering.NewImagePriceDirectory(prices)
	// 序列化「读取→校验候选目录→持久化→发布」，避免并发改价丢更新或发布旧图。
	var mu sync.Mutex
	update := func(alias string, rec *store.ImagePricingRecord) error {
		mu.Lock()
		defer mu.Unlock()
		all, err := merge(alias, rec, true)
		if err != nil {
			return err
		}
		prices, err := imagePrices(all, settlement, fx)
		if err != nil {
			return store.MarkBusiness(err)
		}
		if rec != nil {
			err = store.UpsertImagePricing(db, *rec)
		} else {
			_, err = store.DeleteImagePricing(db, alias)
		}
		if err != nil {
			return err
		}
		dir.Replace(prices)
		return nil
	}
	return dir, static, update, nil
}
