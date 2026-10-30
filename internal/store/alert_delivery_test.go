// 告警投递自监控单行语义:失败落库、失败期计数推进、成功解除保留事实、解除后再失败重新计数、
// 从未失败返回 nil。
package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAlertDelivery_失败落库与失败期推进(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "muniu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if err := RecordAlertDeliveryFailure(db, "balance_tier:p1", "warning", "x", "http_status_503", at); err != nil {
		t.Fatal(err)
	}
	st, err := GetAlertDeliveryStatus(db)
	if err != nil || st == nil {
		t.Fatalf("查得失败态: st=%v err=%v", st, err)
	}
	if !st.Failing || st.FailureCount != 1 || st.LastEventKey != "balance_tier:p1" || st.LastError != "http_status_503" {
		t.Fatalf("首笔失败事实不符: %+v", st)
	}
	// 失败期内重复失败:推进计数不新增行
	if err := RecordAlertDeliveryFailure(db, "keypool_exhausted:p2", "warning", "y", "network", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	st, _ = GetAlertDeliveryStatus(db)
	if !st.Failing || st.FailureCount != 2 || st.LastEventKey != "keypool_exhausted:p2" {
		t.Fatalf("失败期推进不符: %+v", st)
	}
}

func TestAlertDelivery_成功解除保留事实_再失败重新计数(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "muniu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	_ = RecordAlertDeliveryFailure(db, "e", "warning", "s", "network", at)
	// 未失败期先到一笔成功:必须 no-op(不凭空造行)
	if err := RecordAlertDeliverySuccess(db, at); err != nil {
		t.Fatal(err)
	}
	if st, _ := GetAlertDeliveryStatus(db); st == nil {
		t.Fatal("成功 no-op 不应影响既有失败行")
	}
	_ = RecordAlertDeliveryFailure(db, "e2", "warning", "s2", "network", at.Add(time.Minute))
	if err := RecordAlertDeliverySuccess(db, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	st, _ := GetAlertDeliveryStatus(db)
	if st.Failing || st.LastResolvedAt.IsZero() || st.LastFailureAt.IsZero() || st.LastEventKey != "e2" {
		t.Fatalf("解除态不符(事实应保留): %+v", st)
	}
	// 解除后再失败:新失败期,计数重置为 1
	if err := RecordAlertDeliveryFailure(db, "e3", "warning", "s3", "network", at.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	st, _ = GetAlertDeliveryStatus(db)
	if !st.Failing || st.FailureCount != 1 {
		t.Fatalf("新失败期计数应重置: %+v", st)
	}
}

func TestAlertDelivery_从未失败返回nil(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "muniu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	st, err := GetAlertDeliveryStatus(db)
	if err != nil || st != nil {
		t.Fatalf("无行应返回 nil,nil: st=%v err=%v", st, err)
	}
}

// last_error 为 NOT NULL 列:空错误归类必须落库为空串而非 NULL(约束违反会丢失失败事实)。
func TestAlertDelivery_空错误归类落库(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "muniu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if err := RecordAlertDeliveryFailure(db, "e", "warning", "s", "", at); err != nil {
		t.Fatalf("空错误归类应正常落库: %v", err)
	}
	st, err := GetAlertDeliveryStatus(db)
	if err != nil || st == nil || !st.Failing || st.LastError != "" {
		t.Fatalf("空错误归类事实不符: st=%+v err=%v", st, err)
	}
}
