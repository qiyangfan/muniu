// metering_buffer repo(ADR-0007):ingest 汇失败时的本地缓冲(计量行绝不丢)。
// 语义:Save 带容量上限(满返回 ErrBufferFull,调用方阻塞重试形成背压);
// Pending 只读不删(先投远端,成功后 Delete——崩溃窗口由远端按 row_id 幂等去重兜底)。
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// toAnyPlaceholders 生成 N 个占位符。
func toAnyPlaceholders(n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = "?"
	}
	return strings.Join(out, ",")
}

// toAnySlice []int64 → []any(SQL 变参)。
func toAnySlice(vs []int64) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// ErrBufferFull 缓冲已达容量上限(哨兵;调用方阻塞重试形成背压)。
var ErrBufferFull = errors.New("store: 计量缓冲已满")

// BufferedRow 缓冲行(序号保序 + 原始计量行)。
type BufferedRow struct {
	Seq int64
	Row UsageRow
}

// SaveBufferedRows 批量入缓冲(容量检查与写入同事务)。
func SaveBufferedRows(db *sql.DB, capacity int, rows []UsageRow) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: 开启缓冲事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM metering_buffer`).Scan(&n); err != nil {
		return fmt.Errorf("store: 查缓冲水位: %w", err)
	}
	if n+int64(len(rows)) > int64(capacity) {
		return ErrBufferFull
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range rows {
		payload, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("store: 序列化缓冲行: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO metering_buffer (row_id, payload, created_at) VALUES (?,?,?)`,
			r.ID, string(payload), now); err != nil {
			return fmt.Errorf("store: 写缓冲行: %w", err)
		}
	}
	return tx.Commit()
}

// PendingBufferedRows 按序读取待续传行(只读不删;投递成功后按 Seq 调 DeleteBufferedRows)。
// 反序列化失败的毒丸行(跨版本 schema 变更/存储损坏)自愈式剔除并计入 returnedSkipped,
// 调用方必须告警——不剔除会让该行之后的全部缓冲永久阻塞,违背续传契约。
func PendingBufferedRows(db *sql.DB, limit int) (out []BufferedRow, skipped []int64, err error) {
	rows, err := db.Query(`SELECT seq, payload FROM metering_buffer ORDER BY seq LIMIT ?`, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("store: 读缓冲: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var deadSeqs []int64
	for rows.Next() {
		var seq int64
		var payload string
		if err := rows.Scan(&seq, &payload); err != nil {
			return nil, nil, fmt.Errorf("store: 扫缓冲行: %w", err)
		}
		var r UsageRow
		if uerr := json.Unmarshal([]byte(payload), &r); uerr != nil {
			// 毒丸行(跨版本 schema 变更/存储损坏):迭代期只收集——生产库 MaxOpenConns(1),
			// 游标未关时 DELETE 会在唯一连接上永久等待;关游标后统一剔除
			deadSeqs = append(deadSeqs, seq)
			skipped = append(skipped, seq)
			continue
		}
		out = append(out, BufferedRow{Seq: seq, Row: r})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("store: 遍历缓冲: %w", err)
	}
	_ = rows.Close() // 显式释放游标,腾出唯一连接供删除使用
	if len(deadSeqs) > 0 {
		if _, err := db.Exec(`DELETE FROM metering_buffer WHERE seq IN (`+toAnyPlaceholders(len(deadSeqs))+`)`,
			toAnySlice(deadSeqs)...); err != nil {
			return nil, nil, fmt.Errorf("store: 剔除毒丸行: %w", err)
		}
	}
	return out, skipped, nil
}

// DeleteBufferedRows 按序号删除已续传行。
func DeleteBufferedRows(db *sql.DB, seqs []int64) error {
	if len(seqs) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: 开启缓冲删除事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, seq := range seqs {
		if _, err := tx.Exec(`DELETE FROM metering_buffer WHERE seq = ?`, seq); err != nil {
			return fmt.Errorf("store: 删缓冲行: %w", err)
		}
	}
	return tx.Commit()
}

// CountBufferedRows 缓冲水位(观测/容量判定)。
func CountBufferedRows(db *sql.DB) (int64, error) {
	var n int64
	err := db.QueryRow(`SELECT COUNT(*) FROM metering_buffer`).Scan(&n)
	return n, err
}
