// model_pricing repo：动态改价的运行态价目 CRUD。
// YAML pricing 为静态兜底，本表按别名覆盖；读写均为整数 credit，窗口以 JSON 形态持久化。
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// PricingWindowRecord 时段窗持久化形态，window 为 "HH:MM-HH:MM" 原文，基点为已解析倍率。
type PricingWindowRecord struct {
	Window      string `json:"window"`
	BasisPoints int64  `json:"basis_points"`
}

// PricingRecord model_pricing 行，管理面读侧与写侧共用形态。
type PricingRecord struct {
	Alias              string
	Currency           string
	InputCredits       int64
	CachedInputCredits int64 // 0=缺省同 input
	VisionInputCredits int64 // 0=缺省同 input
	OutputCredits      int64
	Timezone           string
	Windows            []PricingWindowRecord
	UpdatedAt          string
}

// ListPricing 全部运行态价目，按别名升序。
func ListPricing(db *sql.DB) ([]PricingRecord, error) {
	rows, err := db.Query(`SELECT alias, currency, input_credits, cached_input_credits, vision_input_credits, output_credits,
		timezone, time_windows, updated_at FROM model_pricing ORDER BY alias`)
	if err != nil {
		return nil, fmt.Errorf("store: 列出价目: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PricingRecord
	for rows.Next() {
		var r PricingRecord
		var windowsJSON string
		if err := rows.Scan(&r.Alias, &r.Currency, &r.InputCredits, &r.CachedInputCredits, &r.VisionInputCredits,
			&r.OutputCredits, &r.Timezone, &windowsJSON, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: 扫价目行: %w", err)
		}
		if err := json.Unmarshal([]byte(windowsJSON), &r.Windows); err != nil {
			return nil, fmt.Errorf("store: 解析价目窗: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetPricing 按别名取运行态价目；未配置返回 ErrNotFound。
func GetPricing(db *sql.DB, alias string) (*PricingRecord, error) {
	row := db.QueryRow(`SELECT alias, currency, input_credits, cached_input_credits, vision_input_credits, output_credits,
		timezone, time_windows, updated_at FROM model_pricing WHERE alias = ?`, alias)
	var r PricingRecord
	var windowsJSON string
	err := row.Scan(&r.Alias, &r.Currency, &r.InputCredits, &r.CachedInputCredits, &r.VisionInputCredits,
		&r.OutputCredits, &r.Timezone, &windowsJSON, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查价目: %w", err)
	}
	if err := json.Unmarshal([]byte(windowsJSON), &r.Windows); err != nil {
		return nil, fmt.Errorf("store: 解析价目窗: %w", err)
	}
	return &r, nil
}

// UpsertPricing 写入或覆盖运行态价目，同别名覆盖。
func UpsertPricing(db *sql.DB, r PricingRecord) error {
	windowsJSON, err := json.Marshal(r.Windows)
	if err != nil {
		return fmt.Errorf("store: 序列化价目窗: %w", err)
	}
	if r.Timezone == "" {
		r.Timezone = "UTC"
	}
	_, err = db.Exec(`INSERT INTO model_pricing
		(alias, currency, input_credits, cached_input_credits, vision_input_credits, output_credits, timezone, time_windows, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(alias) DO UPDATE SET
		  currency=excluded.currency, input_credits=excluded.input_credits,
		  cached_input_credits=excluded.cached_input_credits, output_credits=excluded.output_credits,
		  vision_input_credits=excluded.vision_input_credits,
		  timezone=excluded.timezone, time_windows=excluded.time_windows, updated_at=excluded.updated_at`,
		r.Alias, r.Currency, r.InputCredits, r.CachedInputCredits, r.VisionInputCredits, r.OutputCredits,
		r.Timezone, string(windowsJSON), r.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: 写价目: %w", err)
	}
	return nil
}

// DeletePricing 删除运行态价目，该别名回落 YAML 静态兜底；返回删除行数。
func DeletePricing(db *sql.DB, alias string) (int64, error) {
	res, err := db.Exec(`DELETE FROM model_pricing WHERE alias = ?`, alias)
	if err != nil {
		return 0, fmt.Errorf("store: 删价目: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
