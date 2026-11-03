// keypool 冷却持久化访问器契约:保存/加载往返、清除语义(bucket 空删行)、孤儿对账。
package store

import (
	"testing"
	"time"
)

func TestKeyCooldown_保存加载往返与清除(t *testing.T) {
	db := openTestStore(t)
	now := time.Now()

	if err := SaveKeyCooldown(db, "pa", "K1", "credit", now.Add(30*time.Minute), 1, now, 1); err != nil {
		t.Fatalf("保存: %v", err)
	}
	if err := SaveKeyCooldown(db, "pa", "K2", "rate", now.Add(8*time.Minute), 4, now, 2); err != nil {
		t.Fatalf("保存: %v", err)
	}
	rows, err := LoadKeyCooldowns(db, "pa")
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应两行: %+v", rows)
	}
	byID := map[string]KeyCooldownRow{}
	for _, r := range rows {
		byID[r.KeyID] = r
	}
	if r := byID["K1"]; r.Bucket != "credit" || r.Consec != 1 || !r.Until.After(now.Add(29*time.Minute)) {
		t.Fatalf("K1 行失真: %+v", r)
	}
	if r := byID["K2"]; r.Bucket != "rate" || r.Consec != 4 {
		t.Fatalf("K2 行失真: %+v", r)
	}

	// 清除语义:空 bucket = 删行
	if err := SaveKeyCooldown(db, "pa", "K1", "", time.Time{}, 0, now, 2); err != nil {
		t.Fatalf("清除: %v", err)
	}
	rows, _ = LoadKeyCooldowns(db, "pa")
	if len(rows) != 1 || rows[0].KeyID != "K2" {
		t.Fatalf("K1 应被清除: %+v", rows)
	}

	// 孤儿对账:keep 之外的行删除
	if err := PruneKeyCooldowns(db, "pa", map[string]bool{"K2": true}); err != nil {
		t.Fatalf("对账: %v", err)
	}
	if rows, _ = LoadKeyCooldowns(db, "pa"); len(rows) != 1 {
		t.Fatalf("对账后应剩一行: %+v", rows)
	}
	// 其他渠道不受影响
	_ = SaveKeyCooldown(db, "pb", "K9", "credit", now.Add(time.Hour), 1, now, 1)
	_ = PruneKeyCooldowns(db, "pa", nil)
	if rows, _ = LoadKeyCooldowns(db, "pb"); len(rows) != 1 {
		t.Fatalf("跨渠道不得误删: %+v", rows)
	}
}

// TestKeyCooldown_乱序守卫 评审 B6 哨兵:同键两个事件的落库完成顺序可颠倒(锁外同步写),
// 行状态必须由事件序号单调裁决——迟到的旧进入(UPSERT)不得覆盖新释放(DELETE),
// 迟到的旧释放不得覆盖新进入。序号守卫移除时本用例必红。
func TestKeyCooldown_乱序守卫(t *testing.T) {
	db := openTestStore(t)
	now := time.Now()

	// 新事件:进入(seq=2)
	if err := SaveKeyCooldown(db, "pa", "K", "credit", now.Add(30*time.Minute), 1, now, 2); err != nil {
		t.Fatalf("保存: %v", err)
	}
	// 迟到旧事件:释放(seq=1)→ 守卫应忽略
	if err := SaveKeyCooldown(db, "pa", "K", "", time.Time{}, 0, now, 1); err != nil {
		t.Fatalf("旧释放: %v", err)
	}
	rows, _ := LoadKeyCooldowns(db, "pa")
	if len(rows) != 1 || rows[0].Bucket != "credit" {
		t.Fatalf("旧释放不得覆盖新进入: %+v", rows)
	}

	// 迟到旧事件:更早的进入(seq=1)→ 守卫应忽略
	if err := SaveKeyCooldown(db, "pa", "K", "rate", now.Add(time.Minute), 3, now, 1); err != nil {
		t.Fatalf("旧进入: %v", err)
	}
	rows, _ = LoadKeyCooldowns(db, "pa")
	if len(rows) != 1 || rows[0].Bucket != "credit" || rows[0].Consec != 1 {
		t.Fatalf("旧进入不得覆盖新进入: %+v", rows)
	}

	// 更新事件(seq=3):释放 → 生效删行
	if err := SaveKeyCooldown(db, "pa", "K", "", time.Time{}, 0, now, 3); err != nil {
		t.Fatalf("新释放: %v", err)
	}
	if rows, _ = LoadKeyCooldowns(db, "pa"); len(rows) != 0 {
		t.Fatalf("新释放应删行: %+v", rows)
	}
}
