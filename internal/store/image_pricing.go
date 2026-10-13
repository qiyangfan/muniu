package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// ImagePricingRecord 持久化报价形态，换算本位币发生在目录重建层。
type ImagePricingRecord struct {
	Alias          string
	Currency       string
	DefaultCredits int64
	Tiers          map[string]int64
	UpdatedAt      string
}

// ListImagePricing 脏 JSON 拒绝整目录读取。
func ListImagePricing(db *sql.DB) ([]ImagePricingRecord, error) {
	rows, err := db.Query(`SELECT alias,currency,default_credits,tiers,updated_at FROM image_pricing ORDER BY alias`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ImagePricingRecord{}
	for rows.Next() {
		var r ImagePricingRecord
		var raw string
		if err := rows.Scan(&r.Alias, &r.Currency, &r.DefaultCredits, &raw, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &r.Tiers); err != nil || r.Tiers == nil {
			return nil, fmt.Errorf("store: images 档位 JSON 非法(%s)", r.Alias)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertImagePricing 按别名整体覆盖，不逐档合并。
func UpsertImagePricing(db *sql.DB, r ImagePricingRecord) error {
	if r.Tiers == nil {
		r.Tiers = map[string]int64{}
	}
	b, err := json.Marshal(r.Tiers)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO image_pricing VALUES(?,?,?,?,?) ON CONFLICT(alias) DO UPDATE SET currency=excluded.currency,default_credits=excluded.default_credits,tiers=excluded.tiers,updated_at=excluded.updated_at`, r.Alias, r.Currency, r.DefaultCredits, string(b), r.UpdatedAt)
	return err
}

// DeleteImagePricing 删除运行态覆盖，回落由目录重建完成。
func DeleteImagePricing(db *sql.DB, alias string) (int64, error) {
	res, err := db.Exec(`DELETE FROM image_pricing WHERE alias=?`, alias)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
