package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// AdminAudit 行只增不改，时间与 ID 共同构成稳定排序。
type AdminAudit struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
	ActorKind string `json:"actor_kind"`
	ActorName string `json:"actor_name"`
	Action    string `json:"action"`
	TargetID  string `json:"target_id"`
	Outcome   string `json:"outcome"`
	Detail    string `json:"detail"`
	CreatedAt string `json:"created_at"`
}

// AppendAdminAudit 不提供更新或删除接口。
func AppendAdminAudit(ctx context.Context, db *sql.DB, a AdminAudit) error {
	_, err := db.ExecContext(ctx, `INSERT INTO admin_audit_log VALUES(?,?,?,?,?,?,?,?,?)`,
		a.ID, a.RequestID, a.ActorKind, a.ActorName, a.Action, a.TargetID, a.Outcome, a.Detail, a.CreatedAt)
	return err
}

type auditCursor struct {
	Time string `json:"t"`
	ID   string `json:"i"`
}

// adminAuditCols 审计行读取列清单，单行与翻页共用。
const adminAuditCols = `id,request_id,actor_kind,actor_name,action,
	coalesce(target_id,''),outcome,coalesce(detail,''),created_at`

// ListAdminAudit 同秒行按 UUIDv7 排序，游标不因并发新增而重复旧行。
// items 最多 limit 条；多取的一行只用于生成 next_cursor。
func ListAdminAudit(ctx context.Context, db *sql.DB, cursor string, limit int) ([]AdminAudit, string, error) {
	if limit < 1 || limit > 100 {
		return nil, "", MarkBusiness(fmt.Errorf("limit 须在 1 到 100 之间"))
	}
	c, err := decodeAuditCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	out, err := queryAdminAudit(ctx, db, c, limit)
	if err != nil {
		return nil, "", err
	}
	next, err := nextAuditCursor(out[limit:])
	if err != nil {
		return nil, "", err
	}
	return out[:limit], next, nil
}

// decodeAuditCursor 解析翻页游标；空游标得零值（首页）。
func decodeAuditCursor(cursor string) (auditCursor, error) {
	c := auditCursor{}
	if cursor == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(b) > 1024 {
		return c, MarkBusiness(fmt.Errorf("cursor 非法"))
	}
	if err := json.Unmarshal(b, &c); err != nil || c.Time == "" || c.ID == "" {
		return c, MarkBusiness(fmt.Errorf("cursor 非法"))
	}
	return c, nil
}

// queryAdminAudit 按游标条件取 limit+1 行，多出的一行仅用于判定是否还有下一页。
func queryAdminAudit(ctx context.Context, db *sql.DB, c auditCursor, limit int) ([]AdminAudit, error) {
	query := `SELECT ` + adminAuditCols + ` FROM admin_audit_log`
	args := []any{}
	if c.Time != "" {
		query += ` WHERE created_at<? OR (created_at=? AND id<?)`
		args = append(args, c.Time, c.Time, c.ID)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AdminAudit{}
	for rows.Next() {
		var a AdminAudit
		if err := rows.Scan(&a.ID, &a.RequestID, &a.ActorKind, &a.ActorName, &a.Action,
			&a.TargetID, &a.Outcome, &a.Detail, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// nextAuditCursor 由越界多取的行生成下一页游标；没有多取行即已到末页。
func nextAuditCursor(overflow []AdminAudit) (string, error) {
	if len(overflow) == 0 {
		return "", nil
	}
	b, err := json.Marshal(auditCursor{Time: overflow[0].CreatedAt, ID: overflow[0].ID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
