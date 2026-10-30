package store

import (
	"database/sql"
	"fmt"
	"time"
)

// AlertDeliveryStatus alert_delivery_status 单行:webhook 投递最终失败的自监控事实。
// 单行取舍:失败期(episode)内推进计数与最近事实,解除保留失败事实;新失败期重置计数——
// 消费方只关心「通道是否活着」与最近一次失败事实,逐事件明细归日志。
type AlertDeliveryStatus struct {
	Failing        bool
	LastEventKey   string
	LastSeverity   string
	LastSummary    string
	LastError      string
	FailureCount   int64
	LastFailureAt  time.Time
	LastResolvedAt time.Time
	UpdatedAt      time.Time
}

// RecordAlertDeliveryFailure 记录投递最终失败:failing 期内推进计数,新失败期重置为 1
// 并清空上次解除时刻(它属于上一个 episode)。
func RecordAlertDeliveryFailure(db *sql.DB, eventKey, severity, summary, errClass string, at time.Time) error {
	_, err := db.Exec(`INSERT INTO alert_delivery_status (
		id, failing, last_event_key, last_severity, last_summary, last_error,
		failure_count, last_failure_at, last_resolved_at, updated_at)
		VALUES (1,1,?,?,?,?,1,?,'',?)
		ON CONFLICT(id) DO UPDATE SET
		failing=1,
		last_event_key=excluded.last_event_key, last_severity=excluded.last_severity,
		last_summary=excluded.last_summary, last_error=excluded.last_error,
		failure_count=CASE WHEN alert_delivery_status.failing=1
			THEN alert_delivery_status.failure_count+1 ELSE 1 END,
		last_failure_at=excluded.last_failure_at, last_resolved_at='',
		updated_at=excluded.updated_at`,
		eventKey, severity, summary, errClass, balanceTime(at), balanceTime(at))
	if err != nil {
		return fmt.Errorf("store: 记录告警投递失败: %w", err)
	}
	return nil
}

// RecordAlertDeliverySuccess 记录投递成功(2xx):仅 failing 期间置解除并保留失败事实;
// 未失败或已解除时 no-op(成功是常态,不产生写放大)。
func RecordAlertDeliverySuccess(db *sql.DB, at time.Time) error {
	_, err := db.Exec(`UPDATE alert_delivery_status
		SET failing=0, last_resolved_at=?, updated_at=?
		WHERE id=1 AND failing=1`, balanceTime(at), balanceTime(at))
	if err != nil {
		return fmt.Errorf("store: 记录告警投递成功: %w", err)
	}
	return nil
}

// GetAlertDeliveryStatus 读取自监控行;从未失败(无行)返回 nil 而非零值结构,
// 调用方据此区分「从未失败」与「零值」。
func GetAlertDeliveryStatus(db *sql.DB) (*AlertDeliveryStatus, error) {
	var st AlertDeliveryStatus
	var failing int64
	var failureAt, resolvedAt, updated sql.NullString
	err := db.QueryRow(`SELECT failing, last_event_key, last_severity, last_summary, last_error,
		failure_count, last_failure_at, last_resolved_at, updated_at
		FROM alert_delivery_status WHERE id=1`).
		Scan(&failing, &st.LastEventKey, &st.LastSeverity, &st.LastSummary, &st.LastError,
			&st.FailureCount, &failureAt, &resolvedAt, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查询告警投递状态: %w", err)
	}
	st.Failing = failing == 1
	for _, p := range []struct {
		s sql.NullString
		t *time.Time
	}{{failureAt, &st.LastFailureAt}, {resolvedAt, &st.LastResolvedAt}, {updated, &st.UpdatedAt}} {
		if p.s.Valid && p.s.String != "" {
			t, perr := parseBalanceTime(p.s.String)
			if perr != nil {
				return nil, fmt.Errorf("store: 告警投递时刻非法 %q: %w", p.s.String, perr)
			}
			*p.t = t
		}
	}
	return &st, nil
}
