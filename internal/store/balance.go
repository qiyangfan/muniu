package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ruleKeyCliffPrefix 逐 key 骤降规则键前缀(与 balance 包 RuleKeyCliffPrefix 同形态;
// store 不依赖 balance,字面量单点在本文件)。
const ruleKeyCliffPrefix = "cliff:"

// AccountKeyID 账户级余额源(manual/aliyun-bss/volc-bss)在 provider_balances 的 key_id 哨兵:
// 账户余额不归属任何单把 key,与 Bearer 源的逐 key 行共用同一张表与查询路径。
const AccountKeyID = "__account__"

// BalanceSnapshot provider_balances 行:每渠道每 key 的最新余额快照。
// BalanceCredits 为 nil 表示余额不可知(openrouter 不限额键、或首次成功查询前)。
// Prev* 为「上一次快照」(最近一次相邻转移的前值,成功写入时平移):骤降判定判据;
// 零值 = 尚无转移可判(首次成功写入前)。PrevCurrency 非零值时才参与币种一致性判定。
type BalanceSnapshot struct {
	Provider            string
	KeyID               string
	Source              string
	BalanceCredits      *int64
	Currency            string
	AsOfAt              time.Time
	PrevBalanceCredits  *int64
	PrevCurrency        string
	PrevAsOfAt          time.Time
	ManualAmount        string
	LastError           string
	ConsecutiveFailures int64
	UpdatedAt           time.Time
}

// HasPrevTransition 前值转移是否可判:无前值列或前值余额为 nil 均不可判。
func (s BalanceSnapshot) HasPrevTransition() bool {
	return s.PrevBalanceCredits != nil
}

// BurnDay 燃速窗口内单日消耗聚合;Day 为 usage_logs.created_at 的日期段(UTC)。
type BurnDay struct {
	Day     string
	Credits int64
}

// AlertState balance_alert_states 行:每渠道每规则(绝对阈值/档位)的告警状态。
type AlertState struct {
	Provider    string
	RuleKey     string
	Firing      bool
	Severity    string
	LastFiredAt *time.Time
	ContextJSON string
	UpdatedAt   time.Time
}

// balanceTime 列存储形态:秒级 RFC3339 UTC,与 usage_logs.created_at 同形态,
// 字符串序比较成立;截断到秒避免亚秒精度破坏字典序 chronological 语义。
func balanceTime(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

func parseBalanceTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

// RecordBalanceSuccess 记录成功查询的快照并清零失败计数;upsert 覆盖余额/币种/来源/时刻。
// 前值平移:覆盖前把既有余额/币种/时刻挪入 prev_*(相邻转移判据);首次写入无既有值,
// prev_balance_credits 保持 NULL(HasPrevTransition=false,无转移可判)。
func RecordBalanceSuccess(ctx context.Context, db *sql.DB, snap BalanceSnapshot) error {
	_, err := db.ExecContext(ctx, `INSERT INTO provider_balances (
		provider, key_id, source, balance_credits, currency, as_of_at,
		prev_balance_credits, prev_currency, prev_as_of_at,
		manual_amount, last_error, consecutive_failures, updated_at)
		VALUES (?,?,?,?,?,?,NULL,'','',?,NULL,0,?)
		ON CONFLICT(provider, key_id) DO UPDATE SET
		source=excluded.source,
		prev_balance_credits=provider_balances.balance_credits,
		prev_currency=provider_balances.currency, prev_as_of_at=provider_balances.as_of_at,
		balance_credits=excluded.balance_credits, currency=excluded.currency,
		as_of_at=excluded.as_of_at, manual_amount=excluded.manual_amount,
		last_error=NULL, consecutive_failures=0, updated_at=excluded.updated_at`,
		snap.Provider, snap.KeyID, snap.Source, snap.BalanceCredits, snap.Currency,
		balanceTime(snap.AsOfAt), nullIfEmpty(snap.ManualAmount), balanceTime(snap.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: 写余额快照 %s/%s: %w", snap.Provider, snap.KeyID, err)
	}
	return nil
}

// RecordBalanceFailure 记录失败:既有快照余额保持不变,仅累加失败计数与错误归类;
// 首次查询即失败时落一行无余额占位,让失败计数有载体。
func RecordBalanceFailure(ctx context.Context, db *sql.DB,
	provider, keyID, source, errClass string, now time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO provider_balances (
		provider, key_id, source, balance_credits, currency, as_of_at,
		last_error, consecutive_failures, updated_at)
		VALUES (?,?,?,NULL,'','',?,1,?)
		ON CONFLICT(provider, key_id) DO UPDATE SET
		last_error=excluded.last_error, consecutive_failures=consecutive_failures+1,
		updated_at=excluded.updated_at`,
		provider, keyID, source, nullIfEmpty(errClass), balanceTime(now))
	if err != nil {
		return fmt.Errorf("store: 记余额失败 %s/%s: %w", provider, keyID, err)
	}
	return nil
}

// ListBalanceSnapshots 列出快照;provider 为空 = 全渠道,非空 = 单渠道(含不存在时的空集)。
func ListBalanceSnapshots(ctx context.Context, db *sql.DB, provider string) ([]BalanceSnapshot, error) {
	q := `SELECT provider, key_id, source, balance_credits, currency, as_of_at,
		prev_balance_credits, prev_currency, prev_as_of_at,
		manual_amount, last_error, consecutive_failures, updated_at FROM provider_balances`
	args := []any{}
	if provider != "" {
		q += ` WHERE provider = ?`
		args = append(args, provider)
	}
	q += ` ORDER BY provider, key_id`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 列余额快照: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BalanceSnapshot
	for rows.Next() {
		s, err := scanBalanceSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// scanBalanceSnapshot 扫描一行快照并解析可空时间列；非法时间列即整查询报错。
func scanBalanceSnapshot(rows *sql.Rows) (BalanceSnapshot, error) {
	var s BalanceSnapshot
	var credits, prevCredits sql.NullInt64
	var asOf, updated, manual, lastErr, prevAsOf sql.NullString
	if err := rows.Scan(&s.Provider, &s.KeyID, &s.Source, &credits, &s.Currency, &asOf,
		&prevCredits, &s.PrevCurrency, &prevAsOf,
		&manual, &lastErr, &s.ConsecutiveFailures, &updated); err != nil {
		return s, fmt.Errorf("store: 扫余额快照: %w", err)
	}
	if credits.Valid {
		v := credits.Int64
		s.BalanceCredits = &v
	}
	if prevCredits.Valid {
		v := prevCredits.Int64
		s.PrevBalanceCredits = &v
	}
	if err := parseIntoTime(prevAsOf, &s.PrevAsOfAt, "prev_as_of_at"); err != nil {
		return s, err
	}
	if manual.Valid {
		s.ManualAmount = manual.String
	}
	if lastErr.Valid {
		s.LastError = lastErr.String
	}
	if err := parseIntoTime(asOf, &s.AsOfAt, "as_of_at"); err != nil {
		return s, err
	}
	if err := parseIntoTime(updated, &s.UpdatedAt, "updated_at"); err != nil {
		return s, err
	}
	return s, nil
}

// parseIntoTime NULL 或空串保持零值,否则按 RFC3339 解析并落到 dst;错误文案带列名。
func parseIntoTime(v sql.NullString, dst *time.Time, col string) error {
	if !v.Valid || v.String == "" {
		return nil
	}
	t, err := parseBalanceTime(v.String)
	if err != nil {
		return fmt.Errorf("store: %s 非法 %q: %w", col, v.String, err)
	}
	*dst = t
	return nil
}

// SpendSince 渠道自某 UTC 时刻起的已结算消耗(credit):有效余额衰减的减项。
// 仅计 estimated=0 行,与 SumCostToday 同口径;as_of_at 为空串时不做时刻过滤(全量)。
func SpendSince(ctx context.Context, db *sql.DB, provider, sinceUTC string) (int64, error) {
	q := `SELECT COALESCE(SUM(cost_credits),0) FROM usage_logs
		WHERE provider = ? AND estimated = 0`
	args := []any{provider}
	if sinceUTC != "" {
		q += ` AND created_at > ?`
		args = append(args, sinceUTC)
	}
	var total int64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("store: 快照后消耗聚合: %w", err)
	}
	return total, nil
}

// BurnWindow 燃速窗口内逐日已结算消耗,日期升序;零行 = 窗口内零消耗。
func BurnWindow(ctx context.Context, db *sql.DB, provider, fromUTC string) ([]BurnDay, error) {
	return BurnWindowRange(ctx, db, provider, fromUTC, "")
}

// BurnWindowRange 区间 [fromUTC, toUTC) 内逐日已结算消耗,toUTC 为空 = 不设上界;
// 零行 = 区间内零消耗。燃速突变告警的基线窗口(上一等长窗口)与当前窗口共用同一聚合形态,
// 逐日行数即「窗口内有消耗天数」,与最小样本判定同源。
func BurnWindowRange(ctx context.Context, db *sql.DB, provider, fromUTC, toUTC string) ([]BurnDay, error) {
	q := `SELECT substr(created_at,1,10) AS day,
		COALESCE(SUM(cost_credits),0) FROM usage_logs
		WHERE provider = ? AND estimated = 0 AND created_at >= ?`
	args := []any{provider, fromUTC}
	if toUTC != "" {
		q += ` AND created_at < ?`
		args = append(args, toUTC)
	}
	q += ` GROUP BY day ORDER BY day`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 燃速窗口聚合: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BurnDay
	for rows.Next() {
		var d BurnDay
		if err := rows.Scan(&d.Day, &d.Credits); err != nil {
			return nil, fmt.Errorf("store: 扫燃速窗口行: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PruneBalanceKeys 清理渠道内已不存在 key 的快照行(账户级哨兵行恒保留):
// 拓扑热替换移除 key 后,残留行会以幽灵余额参与 min-估算、并把渠道级失败计数
// 钉死在 0,两者都是正确性污染而非仅展示问题。同步清理该 key 的逐 key 骤降规则
// 状态行(cliff:<key_id>),防幽灵规则状态残留。
func PruneBalanceKeys(ctx context.Context, db *sql.DB, provider string, keep []string) error {
	if len(keep) == 0 {
		return nil
	}
	if err := deleteStaleBalanceKeys(ctx, db, provider, keep); err != nil {
		return err
	}
	return deleteStaleCliffStates(ctx, db, provider, keep)
}

// deleteStaleBalanceKeys 删掉渠道内不在 keep 中、且非账户哨兵的快照行。
// keep 经 JSON 数组绑定进固定 SQL，避免拼接 IN 列表。
func deleteStaleBalanceKeys(ctx context.Context, db *sql.DB, provider string, keep []string) error {
	arr, err := json.Marshal(keep)
	if err != nil {
		return fmt.Errorf("store: 序列化保留 key: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM provider_balances
		WHERE provider = ? AND key_id != ?
		AND key_id NOT IN (SELECT value FROM json_each(?))`,
		provider, AccountKeyID, string(arr)); err != nil {
		return fmt.Errorf("store: 清理余额残留 key: %w", err)
	}
	return nil
}

// deleteStaleCliffStates 删掉渠道内不在 keep 中的逐 key 骤降规则状态行。
func deleteStaleCliffStates(ctx context.Context, db *sql.DB, provider string, keep []string) error {
	cliffKeys := make([]string, len(keep))
	for i, k := range keep {
		cliffKeys[i] = ruleKeyCliffPrefix + k
	}
	arr, err := json.Marshal(cliffKeys)
	if err != nil {
		return fmt.Errorf("store: 序列化保留规则: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM balance_alert_states
		WHERE provider = ? AND rule_key LIKE 'cliff:%'
		AND rule_key NOT IN (SELECT value FROM json_each(?))`,
		provider, string(arr)); err != nil {
		return fmt.Errorf("store: 清理骤降规则残留状态: %w", err)
	}
	return nil
}

// UpsertAlertState 写告警状态(评估循环与手动清理由此落库)。
func UpsertAlertState(ctx context.Context, db *sql.DB, st AlertState) error {
	var fired any
	if st.LastFiredAt != nil {
		fired = balanceTime(*st.LastFiredAt)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO balance_alert_states (
		provider, rule_key, firing, severity, last_fired_at, context_json, updated_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(provider, rule_key) DO UPDATE SET
		firing=excluded.firing, severity=excluded.severity, last_fired_at=excluded.last_fired_at,
		context_json=excluded.context_json, updated_at=excluded.updated_at`,
		st.Provider, st.RuleKey, boolToInt(st.Firing), st.Severity, fired,
		nullIfEmpty(st.ContextJSON), balanceTime(st.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: 写告警状态 %s/%s: %w", st.Provider, st.RuleKey, err)
	}
	return nil
}

// ListAlertStates 列出告警状态;provider 为空 = 全渠道。
func ListAlertStates(ctx context.Context, db *sql.DB, provider string) ([]AlertState, error) {
	q := `SELECT provider, rule_key, firing, severity, last_fired_at, context_json, updated_at
		FROM balance_alert_states`
	args := []any{}
	if provider != "" {
		q += ` WHERE provider = ?`
		args = append(args, provider)
	}
	q += ` ORDER BY provider, rule_key`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 列告警状态: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AlertState
	for rows.Next() {
		st, err := scanAlertState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// scanAlertState 扫描一行告警状态；last_fired_at 非空时回填指针。
func scanAlertState(rows *sql.Rows) (AlertState, error) {
	var st AlertState
	var firing int64
	var fired, context, updated sql.NullString
	if err := rows.Scan(&st.Provider, &st.RuleKey, &firing, &st.Severity, &fired, &context, &updated); err != nil {
		return st, fmt.Errorf("store: 扫告警状态: %w", err)
	}
	st.Firing = firing == 1
	if fired.Valid && fired.String != "" {
		t, perr := parseBalanceTime(fired.String)
		if perr != nil {
			return st, fmt.Errorf("store: last_fired_at 非法 %q: %w", fired.String, perr)
		}
		st.LastFiredAt = &t
	}
	if context.Valid {
		st.ContextJSON = context.String
	}
	if err := parseIntoTime(updated, &st.UpdatedAt, "updated_at"); err != nil {
		return st, err
	}
	return st, nil
}

// ErrBalanceUnsupported balance 能力未装配时的哨兵,管理端点据此返回禁用态。
var ErrBalanceUnsupported = errors.New("balance: 未装配")

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
