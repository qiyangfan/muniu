// keypool 冷却持久化:保存/加载/对账三件套。仅承载 ≥5min 冷却迁移(credit 桶全程、
// rate 桶后段);keypool 不 import store(store 不依赖 keypool),行形态在此定义,
// 装配层转换为 keypool.CooldownRecord。
//
// 乱序守卫(ev_seq):冷却事件的落库在池锁外执行,同键两事件的完成顺序可颠倒
// (请求 goroutine 的欠费进入 vs 余额回写 goroutine 的释放)。行状态由事件单调序号裁决
// ——迟到的旧事件被守卫忽略,否则重启会复活池内已释放的冷却(无余额源 key 最长 2h 无自愈触发)。
package store

import (
	"database/sql"
	"fmt"
	"math"
	"time"
)

// PruneSeq 对账清除专用序号:恒大于一切事件序号,保证对账删行必生效。
const PruneSeq = math.MaxInt64

// KeyCooldownRow 冷却表行形态(与 keypool 解耦的存储侧投影)。
type KeyCooldownRow struct {
	KeyID  string
	Bucket string
	Until  time.Time
	Consec int
	Seq    int64
}

// SaveKeyCooldown 冷却迁移落库:bucket 为空串 = 清除语义(删行,欠费回写「充值即恢复」
// 的提前释放与成功清零都走这里)。ev_seq 单调守卫:同键新事件覆盖旧事件、旧事件被忽略
// (锁外落库的完成顺序不可控,行状态必须由事件序裁决);幂等(同序号重复迁移无副作用,
// UPSERT 的 WHERE 与 DELETE 的 ev_seq<? 均为严格小于)。
func SaveKeyCooldown(db *sql.DB, provider, keyID, bucket string, until time.Time, consec int, now time.Time, seq int64) error {
	if bucket == "" {
		_, err := db.Exec(`DELETE FROM key_cooldowns
			WHERE provider=? AND key_id=? AND ev_seq<?`, provider, keyID, seq)
		if err != nil {
			return fmt.Errorf("store: 删冷却行 %s/%s: %w", provider, keyID, err)
		}
		return nil
	}
	_, err := db.Exec(`INSERT INTO key_cooldowns
		(provider, key_id, bucket, cool_until, consec, ev_seq, updated_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(provider, key_id) DO UPDATE SET
		bucket=excluded.bucket, cool_until=excluded.cool_until,
		consec=excluded.consec, ev_seq=excluded.ev_seq, updated_at=excluded.updated_at
		WHERE key_cooldowns.ev_seq < excluded.ev_seq`,
		provider, keyID, bucket, cooldownTime(until), consec, seq, cooldownTime(now))
	if err != nil {
		return fmt.Errorf("store: 存冷却行 %s/%s: %w", provider, keyID, err)
	}
	return nil
}

// LoadKeyCooldowns 单渠道全量冷却行(启动恢复);含已到期行,由池侧过期过滤
// (序号仍在行上,池侧须对过期行同样播种序号,否则重启后新事件被旧守卫压住)。
func LoadKeyCooldowns(db *sql.DB, provider string) ([]KeyCooldownRow, error) {
	rows, err := db.Query(`SELECT key_id, bucket, cool_until, consec, ev_seq
		FROM key_cooldowns WHERE provider=?`, provider)
	if err != nil {
		return nil, fmt.Errorf("store: 读冷却行 %s: %w", provider, err)
	}
	defer func() { _ = rows.Close() }()
	var out []KeyCooldownRow
	for rows.Next() {
		var r KeyCooldownRow
		var until string
		if err := rows.Scan(&r.KeyID, &r.Bucket, &until, &r.Consec, &r.Seq); err != nil {
			return nil, fmt.Errorf("store: 扫冷却行 %s/%s: %w", provider, r.KeyID, err)
		}
		t, perr := time.Parse(time.RFC3339, until)
		if perr != nil {
			return nil, fmt.Errorf("store: 冷却时刻形态 %s/%s: %w", provider, r.KeyID, perr)
		}
		r.Until = t
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneKeyCooldowns 孤儿对账:拓扑替换后键集变化时清掉 keep 之外的行(仅限单渠道)。
func PruneKeyCooldowns(db *sql.DB, provider string, keep map[string]bool) error {
	rows, err := LoadKeyCooldowns(db, provider)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !keep[r.KeyID] {
			if err := SaveKeyCooldown(db, provider, r.KeyID, "", time.Time{}, 0, time.Now(), PruneSeq); err != nil {
				return err
			}
		}
	}
	return nil
}

// cooldownTime 与 balance 族同一时间纪律:RFC3339 UTC,秒级截断。
func cooldownTime(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }
