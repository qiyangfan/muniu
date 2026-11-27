// Package breakglass 提供运行时存储的独立修复通道：独立凭证、限时窗口、原因、
// 二次确认、影响行数上限与同事务审计。仅支持文档化修复操作，不接受任意 SQL。
package breakglass

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"uuid"

	"github.com/solvela/muniu/internal/store"
)

// Credential 独立凭证文件形态：token 只存摘要，且带限时窗口。
type Credential struct {
	TokenSHA256 string `json:"token_sha256"`
	NotBefore   string `json:"not_before"`
	NotAfter    string `json:"not_after"`
}

// Options 一次 break-glass 调用。
type Options struct {
	DBPath         string
	CredentialPath string
	Token          string // 由 CLI 从环境变量读取，绝不落盘
	Op             string
	Reason         string
	Params         map[string]any
	MaxRows        int
	Now            time.Time
	// Confirm 二次确认；返回 false 即取消，不连接存储。
	Confirm func(op, summary string) (bool, error)
}

// Report 执行结果。
type Report struct {
	Op         string
	Rows       int64
	SQLSummary string
	AuditID    string
}

// Operation 文档化修复操作。
type Operation struct {
	Name        string
	Description string
	Summary     string
	MaxRowsHard int
}

var operations = map[string]Operation{
	"purge-expired-semantic-cache": {
		Name: "purge-expired-semantic-cache", Description: "清理已过期的语义缓存行",
		Summary: "DELETE FROM semantic_cache WHERE expires_at <= now", MaxRowsHard: 1000,
	},
	"purge-oidc-states": {
		Name: "purge-oidc-states", Description: "清理过期的 OIDC 登录 state（默认 24h 前）",
		Summary: "DELETE FROM oidc_pending_states WHERE created_at < cutoff", MaxRowsHard: 1000,
	},
	"drop-buffered-usage": {
		Name: "drop-buffered-usage", Description: "丢弃指定的计量缓冲毒丸行（seq），会丢失该批计量",
		Summary: "DELETE FROM metering_buffer WHERE seq = ?", MaxRowsHard: 1,
	},
	"clear-key-cooldowns": {
		Name: "clear-key-cooldowns", Description: "清除指定 provider 的本地 key 冷却记录",
		Summary: "DELETE FROM key_cooldowns WHERE provider = ? LIMIT n", MaxRowsHard: 1000,
	},
}

// Operations 返回文档化操作清单（按名称排序稳定输出）。
func Operations() []Operation {
	order := []string{"clear-key-cooldowns", "drop-buffered-usage", "purge-expired-semantic-cache", "purge-oidc-states"}
	out := make([]Operation, 0, len(order))
	for _, name := range order {
		out = append(out, operations[name])
	}
	return out
}

// Run 校验凭证/时间窗/确认后执行；审计与修复同事务，审计失败整体回滚。
func Run(ctx context.Context, opts Options) (Report, error) {
	op, ok := operations[opts.Op]
	if !ok {
		return Report{}, fmt.Errorf("未注册的修复操作 %q", opts.Op)
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	if err := checkCredential(opts, now); err != nil {
		return Report{}, err
	}
	reason := strings.TrimSpace(opts.Reason)
	if reason == "" {
		return Report{}, errors.New("必须填写修复原因")
	}
	if len(reason) > 500 {
		return Report{}, errors.New("原因过长(≤500)")
	}
	maxRows, err := normalizeMaxRows(opts.MaxRows, op.MaxRowsHard)
	if err != nil {
		return Report{}, err
	}
	if opts.Confirm == nil {
		return Report{}, errors.New("缺少二次确认")
	}
	okConfirm, err := opts.Confirm(op.Name, op.Description)
	if err != nil {
		return Report{}, err
	}
	if !okConfirm {
		return Report{}, errors.New("已取消")
	}
	return execute(ctx, opts.DBPath, op, opts.Params, reason, maxRows, now)
}

// CredentialFromFile 读取并解析凭证文件；权限过宽直接拒绝。
func CredentialFromFile(path string) (Credential, error) {
	var c Credential
	info, err := os.Stat(path)
	if err != nil {
		return c, fmt.Errorf("读取 break-glass 凭证: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return c, errors.New("break-glass 凭证文件权限过宽（须 0600）")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // 路径来自运维显式参数
	if err != nil {
		return c, fmt.Errorf("读取 break-glass 凭证: %w", err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("break-glass 凭证非法: %w", err)
	}
	return c, nil
}

// checkCredential 独立 token 摘要恒时比较 + 时间窗。
func checkCredential(opts Options, now time.Time) error {
	if strings.TrimSpace(opts.Token) == "" {
		return errors.New("缺少 break-glass 凭证 token（环境变量 MUNIU_BREAK_GLASS_TOKEN）")
	}
	cred, err := CredentialFromFile(opts.CredentialPath)
	if err != nil {
		return err
	}
	if err := matchToken(cred, opts.Token); err != nil {
		return err
	}
	return checkWindow(cred, now)
}

// matchToken 摘要恒时比较。
func matchToken(cred Credential, token string) error {
	sum := sha256.Sum256([]byte(token))
	want, err := hex.DecodeString(cred.TokenSHA256)
	if err != nil || len(want) != len(sum) {
		return errors.New("break-glass 凭证摘要非法")
	}
	if subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return errors.New("break-glass 凭证无效")
	}
	return nil
}

// checkWindow 限时窗口必须有效且当前在窗内。
func checkWindow(cred Credential, now time.Time) error {
	notBefore, err := time.Parse(time.RFC3339, cred.NotBefore)
	if err != nil {
		return errors.New("break-glass 时间窗 not_before 非法")
	}
	notAfter, err := time.Parse(time.RFC3339, cred.NotAfter)
	if err != nil || !notAfter.After(notBefore) {
		return errors.New("break-glass 时间窗 not_after 非法")
	}
	if now.Before(notBefore) || now.After(notAfter) {
		return fmt.Errorf("不在 break-glass 时间窗内(%s ~ %s)", cred.NotBefore, cred.NotAfter)
	}
	return nil
}

// normalizeMaxRows 行数上限：缺省 100，硬顶按操作。
func normalizeMaxRows(v, hard int) (int, error) {
	if hard == 1 {
		return 1, nil // 单行操作固定上限，CLI 缺省值不构成拒绝
	}
	if v <= 0 {
		v = 100
	}
	if hard > 0 && v > hard {
		return 0, fmt.Errorf("max_rows 超过该操作上限 %d", hard)
	}
	if v > 1000 {
		return 0, errors.New("max_rows 超过全局硬顶 1000")
	}
	return v, nil
}

// execute 单事务修复 + 审计。
func execute(ctx context.Context, dbPath string, op Operation, params map[string]any,
	reason string, maxRows int, now time.Time) (Report, error) {
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return Report{}, fmt.Errorf("打开运行时存储: %w", err)
	}
	defer func() { _ = db.Close() }()
	args, err := opArgs(op.Name, params, maxRows, now)
	if err != nil {
		return Report{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, sqlFor(op.Name), args...)
	if err != nil {
		return Report{}, fmt.Errorf("执行修复: %w", err)
	}
	rows, _ := res.RowsAffected()
	auditID := uuid.NewV7().String()
	detail, _ := json.Marshal(map[string]any{
		"reason": reason, "sql_summary": op.Summary, "rows": rows,
	})
	a := store.AdminAudit{
		ID: auditID, RequestID: auditID, ActorKind: "system", ActorName: "break-glass",
		Action: "break_glass." + op.Name, Outcome: "ok", Detail: string(detail),
		CreatedAt: now.UTC().Format(time.RFC3339),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_audit_log VALUES(?,?,?,?,?,?,?,?,?)`,
		a.ID, a.RequestID, a.ActorKind, a.ActorName, a.Action, nullIfEmpty(a.TargetID),
		a.Outcome, a.Detail, a.CreatedAt); err != nil {
		return Report{}, fmt.Errorf("写审计失败,修复已回滚: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Report{}, fmt.Errorf("提交修复: %w", err)
	}
	return Report{Op: op.Name, Rows: rows, SQLSummary: op.Summary, AuditID: auditID}, nil
}

// sqlFor 固定 SQL 模板；操作名已白名单校验。
func sqlFor(name string) string {
	switch name {
	case "purge-expired-semantic-cache":
		return `DELETE FROM semantic_cache WHERE id IN (
			SELECT id FROM semantic_cache WHERE expires_at <= ? ORDER BY expires_at LIMIT ?)`
	case "purge-oidc-states":
		return `DELETE FROM oidc_pending_states WHERE state_hash IN (
			SELECT state_hash FROM oidc_pending_states WHERE created_at < ? LIMIT ?)`
	case "drop-buffered-usage":
		return `DELETE FROM metering_buffer WHERE seq = ?`
	case "clear-key-cooldowns":
		return `DELETE FROM key_cooldowns WHERE rowid IN (
			SELECT rowid FROM key_cooldowns WHERE provider = ? LIMIT ?)`
	default:
		return ""
	}
}

// opArgs 参数校验与绑定。
func opArgs(name string, params map[string]any, maxRows int, now time.Time) ([]any, error) {
	switch name {
	case "purge-expired-semantic-cache":
		return []any{now.UTC().Format(time.RFC3339Nano), maxRows}, nil
	case "purge-oidc-states":
		return oidcArgs(params, maxRows, now)
	case "drop-buffered-usage":
		return bufferArgs(params)
	case "clear-key-cooldowns":
		return cooldownArgs(params, maxRows)
	default:
		return nil, errors.New("未注册的修复操作")
	}
}

// oidcArgs 过期 OIDC state 清理参数。
func oidcArgs(params map[string]any, maxRows int, now time.Time) ([]any, error) {
	hours := 24
	if raw, ok := params["older_than_hours"]; ok {
		f, ok := raw.(float64)
		if !ok || f < 1 || f > 24*365 {
			return nil, errors.New("older_than_hours 须为 1..8760")
		}
		hours = int(f)
	}
	cutoff := now.Add(-time.Duration(hours) * time.Hour).UTC().Format(time.RFC3339)
	return []any{cutoff, maxRows}, nil
}

// bufferArgs 计量缓冲毒丸行参数。
func bufferArgs(params map[string]any) ([]any, error) {
	raw, ok := params["seq"]
	if !ok {
		return nil, errors.New("drop-buffered-usage 需要 seq")
	}
	f, ok := raw.(float64)
	if !ok || f < 1 {
		return nil, errors.New("seq 须为正整数")
	}
	return []any{int64(f)}, nil
}

// cooldownArgs key 冷却清理参数。
func cooldownArgs(params map[string]any, maxRows int) ([]any, error) {
	raw, _ := params["provider"].(string)
	provider := strings.TrimSpace(raw)
	if provider == "" || len(provider) > 128 {
		return nil, errors.New("clear-key-cooldowns 需要 provider(≤128)")
	}
	return []any{provider, maxRows}, nil
}

// nullIfEmpty 空串落 NULL（target_id 可空）。
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
