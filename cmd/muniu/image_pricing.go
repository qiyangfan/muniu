package main

import (
	"context"
	"database/sql"
	"sync"

	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
)

func imagePrices(
	rows map[string]store.ImagePricingRecord, settlement string, fx int64,
) (map[string]metering.ImagePrice, error) {
	out := map[string]metering.ImagePrice{}
	for alias, r := range rows {
		price := metering.ImagePrice{Currency: r.Currency, DefaultCredits: r.DefaultCredits, Tiers: r.Tiers}
		p, err := metering.SettleImagePrice(price, settlement, fx)
		if err != nil {
			return nil, err
		}
		out[alias] = p
	}
	return out, nil
}

func assembleImagePricing(ctx context.Context, db *sql.DB, cfg *config.Config, settlement string, fx int64) (
	*metering.ImagePriceDirectory, map[string]metering.ImagePrice,
	func(context.Context, string, *store.ImagePricingRecord) error, error,
) {
	static, err := staticImagePrices(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	all, err := mergeImagePricing(ctx, db, static, "", nil, false)
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
	update := func(ctx context.Context, alias string, rec *store.ImagePricingRecord) error {
		mu.Lock()
		defer mu.Unlock()
		return updateImagePricing(ctx, db, dir, static, settlement, fx, alias, rec)
	}
	return dir, static, update, nil
}

// staticImagePrices 解析 YAML 静态图价。
func staticImagePrices(cfg *config.Config) (map[string]metering.ImagePrice, error) {
	static := map[string]metering.ImagePrice{}
	for alias, v := range cfg.ImagePricing {
		p, err := metering.ParseImagePrice(v.Currency, v.Default, v.Tiers)
		if err != nil {
			return nil, err
		}
		static[alias] = p
	}
	return static, nil
}

// mergeImagePricing 合并静态价与运行态覆盖；apply=true 时按 alias 应用变更（删除请求 rec=nil）。
func mergeImagePricing(
	ctx context.Context, db *sql.DB, static map[string]metering.ImagePrice, alias string,
	change *store.ImagePricingRecord, apply bool,
) (map[string]store.ImagePricingRecord, error) {
	all := map[string]store.ImagePricingRecord{}
	for a, p := range static {
		all[a] = store.ImagePricingRecord{Alias: a, Currency: p.Currency, DefaultCredits: p.DefaultCredits, Tiers: p.Tiers}
	}
	rows, err := store.ListImagePricing(ctx, db)
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
	if !apply {
		return all, nil
	}
	if change == nil && !found {
		return nil, store.ErrNotFound
	}
	if change != nil {
		all[alias] = *change
	}
	return all, nil
}

// updateImagePricing 应用单条图价变更：先算候选目录再落库，最后发布。
func updateImagePricing(ctx context.Context, db *sql.DB, dir *metering.ImagePriceDirectory,
	static map[string]metering.ImagePrice,
	settlement string, fx int64, alias string, rec *store.ImagePricingRecord) error {
	all, err := mergeImagePricing(ctx, db, static, alias, rec, true)
	if err != nil {
		return err
	}
	prices, err := imagePrices(all, settlement, fx)
	if err != nil {
		return store.MarkBusiness(err)
	}
	if rec != nil {
		err = store.UpsertImagePricing(ctx, db, *rec)
	} else {
		_, err = store.DeleteImagePricing(ctx, db, alias)
	}
	if err != nil {
		return err
	}
	dir.Replace(prices)
	return nil
}
