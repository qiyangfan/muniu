package store

import (
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
func AppendAdminAudit(db *sql.DB, a AdminAudit) error {
	_, err := db.Exec(`INSERT INTO admin_audit_log VALUES(?,?,?,?,?,?,?,?,?)`, a.ID, a.RequestID, a.ActorKind, a.ActorName, a.Action, a.TargetID, a.Outcome, a.Detail, a.CreatedAt)
	return err
}

type auditCursor struct {
	Time string `json:"t"`
	ID   string `json:"i"`
}

// ListAdminAudit 同秒行按 UUIDv7 排序，游标不因并发新增而重复旧行。
func ListAdminAudit(db *sql.DB, cursor string, limit int) ([]AdminAudit, string, error) {
	if limit < 1 || limit > 100 {
		return nil, "", MarkBusiness(fmt.Errorf("limit 须在 1 到 100 之间"))
	}
	c := auditCursor{}
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(b) > 1024 {
			return nil, "", MarkBusiness(fmt.Errorf("cursor 非法"))
		}
		if err := json.Unmarshal(b, &c); err != nil || c.Time == "" || c.ID == "" {
			return nil, "", MarkBusiness(fmt.Errorf("cursor 非法"))
		}
	}
	query := `SELECT id,request_id,actor_kind,actor_name,action,coalesce(target_id,''),outcome,coalesce(detail,''),created_at FROM admin_audit_log`
	args := []any{}
	if cursor != "" {
		query += ` WHERE created_at<? OR (created_at=? AND id<?)`
		args = append(args, c.Time, c.Time, c.ID)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := []AdminAudit{}
	for rows.Next() {
		var a AdminAudit
		if err := rows.Scan(&a.ID, &a.RequestID, &a.ActorKind, &a.ActorName, &a.Action, &a.TargetID, &a.Outcome, &a.Detail, &a.CreatedAt); err != nil {
			return nil, "", err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		b, err := json.Marshal(auditCursor{Time: last.CreatedAt, ID: last.ID})
		if err != nil {
			return nil, "", err
		}
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, next, nil
}
