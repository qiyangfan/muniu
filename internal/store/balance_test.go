package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openBalanceDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "balance.db"))
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestBalanceSnapshot_成功写入覆盖与不可知余额往返(t *testing.T) {
	db := openBalanceDB(t)
	credits := int64(110_000_000_000)
	now := time.Now()
	if err := RecordBalanceSuccess(db, BalanceSnapshot{
		Provider: "deepseek", KeyID: "DEEPSEEK_API_KEY", Source: "deepseek",
		BalanceCredits: &credits, Currency: "CNY", AsOfAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入快照: %v", err)
	}
	snap, err := ListBalanceSnapshots(db, "deepseek")
	if err != nil || len(snap) != 1 {
		t.Fatalf("列快照: %v, rows=%d", err, len(snap))
	}
	if snap[0].BalanceCredits == nil || *snap[0].BalanceCredits != credits {
		t.Fatalf("余额往返失真: %+v", snap[0])
	}
	if snap[0].ConsecutiveFailures != 0 || snap[0].LastError != "" {
		t.Fatalf("成功路径不应有失败痕迹: %+v", snap[0])
	}

	// 不可知余额(openrouter 不限额):NULL 列往返不得变成 0
	if err := RecordBalanceSuccess(db, BalanceSnapshot{
		Provider: "openrouter", KeyID: "OPENROUTER_API_KEY", Source: "openrouter",
		BalanceCredits: nil, Currency: "USD", AsOfAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写不可知快照: %v", err)
	}
	snap, err = ListBalanceSnapshots(db, "openrouter")
	if err != nil || len(snap) != 1 || snap[0].BalanceCredits != nil {
		t.Fatalf("不可知余额被写为有值: %v %+v", err, snap)
	}
}

func TestBalanceSnapshot_失败保持旧余额且成功清零计数(t *testing.T) {
	db := openBalanceDB(t)
	credits := int64(50_000_000_000)
	now := time.Now()
	if err := RecordBalanceSuccess(db, BalanceSnapshot{
		Provider: "moonshot", KeyID: AccountKeyID, Source: "moonshot",
		BalanceCredits: &credits, Currency: "CNY", AsOfAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入快照: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := RecordBalanceFailure(db, "moonshot", AccountKeyID, "moonshot", "upstream_5xx", now); err != nil {
			t.Fatalf("记失败: %v", err)
		}
	}
	snap, err := ListBalanceSnapshots(db, "moonshot")
	if err != nil || len(snap) != 1 {
		t.Fatalf("列快照: %v rows=%d", err, len(snap))
	}
	if snap[0].BalanceCredits == nil || *snap[0].BalanceCredits != credits {
		t.Fatalf("失败后旧余额必须保持: %+v", snap[0])
	}
	if snap[0].ConsecutiveFailures != 2 || snap[0].LastError != "upstream_5xx" {
		t.Fatalf("失败计数/归类失真: %+v", snap[0])
	}
	// 成功即清零
	if err := RecordBalanceSuccess(db, BalanceSnapshot{
		Provider: "moonshot", KeyID: AccountKeyID, Source: "moonshot",
		BalanceCredits: &credits, Currency: "CNY", AsOfAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("再写快照: %v", err)
	}
	snap, _ = ListBalanceSnapshots(db, "moonshot")
	if snap[0].ConsecutiveFailures != 0 || snap[0].LastError != "" {
		t.Fatalf("成功后失败痕迹未清零: %+v", snap[0])
	}
}

func TestBalanceSnapshot_首次失败落无余额占位(t *testing.T) {
	db := openBalanceDB(t)
	now := time.Now()
	if err := RecordBalanceFailure(db, "volc", AccountKeyID, "volc-bss", "credential", now); err != nil {
		t.Fatalf("记失败: %v", err)
	}
	snap, err := ListBalanceSnapshots(db, "volc")
	if err != nil || len(snap) != 1 {
		t.Fatalf("列快照: %v rows=%d", err, len(snap))
	}
	if snap[0].BalanceCredits != nil || snap[0].ConsecutiveFailures != 1 {
		t.Fatalf("占位行形态失真: %+v", snap[0])
	}
}

func insertUsage(t *testing.T, db *sqlDB, provider string, at time.Time, estimated bool, credits int64) {
	t.Helper()
	if err := InsertUsageBatch(db, []UsageRow{{
		ID: "u-" + provider + at.Format("20060102150405.000000000"), RequestID: "r-" + t.Name(),
		TenantID: "t", APIKeyID: "k", Provider: provider, ModelAlias: "m", UpstreamModel: "m",
		Status: "ok", Estimated: estimated, CostCredits: credits,
		Currency: "CNY", CreatedAt: at,
	}}); err != nil {
		t.Fatalf("插 usage 行: %v", err)
	}
}

func TestSpendSince_口径与边界(t *testing.T) {
	db := openBalanceDB(t)
	asOf := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	insertUsage(t, db, "deepseek", asOf.Add(-2*time.Hour), false, 100) // 快照前:排除
	insertUsage(t, db, "deepseek", asOf.Add(1*time.Hour), false, 35)   // 快照后:计入
	insertUsage(t, db, "deepseek", asOf.Add(2*time.Hour), true, 999)   // estimated=1:排除
	insertUsage(t, db, "other", asOf.Add(1*time.Hour), false, 777)     // 其他渠道:排除
	got, err := SpendSince(db, "deepseek", balanceTime(asOf))
	if err != nil || got != 35 {
		t.Fatalf("SpendSince = %d, %v; 期望 35", got, err)
	}
}

func TestBurnWindow_逐日聚合与窗口外排除(t *testing.T) {
	db := openBalanceDB(t)
	base := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	insertUsage(t, db, "deepseek", base.Add(8*time.Hour), false, 10)    // 10-28
	insertUsage(t, db, "deepseek", base.Add(32*time.Hour), false, 20)   // 10-29
	insertUsage(t, db, "deepseek", base.Add(33*time.Hour), true, 500)   // estimated 排除
	insertUsage(t, db, "deepseek", base.Add(-24*time.Hour), false, 666) // 窗口外排除
	days, err := BurnWindow(db, "deepseek", balanceTime(base))
	if err != nil {
		t.Fatalf("BurnWindow: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("期望两个自然日,实际 %d: %+v", len(days), days)
	}
	if days[0].Day != "2026-10-28" || days[0].Credits != 10 {
		t.Fatalf("首日失真: %+v", days[0])
	}
	if days[1].Day != "2026-10-29" || days[1].Credits != 20 {
		t.Fatalf("次日失真: %+v", days[1])
	}
}

func TestAlertState_往返与覆盖(t *testing.T) {
	db := openBalanceDB(t)
	now := time.Now()
	fired := now
	if err := UpsertAlertState(db, AlertState{
		Provider: "deepseek", RuleKey: "tier:72", Firing: true, Severity: "warning",
		LastFiredAt: &fired, ContextJSON: `{"balance_credits":35}`, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写告警态: %v", err)
	}
	states, err := ListAlertStates(db, "deepseek")
	if err != nil || len(states) != 1 {
		t.Fatalf("列告警态: %v rows=%d", err, len(states))
	}
	st := states[0]
	if !st.Firing || st.Severity != "warning" || st.LastFiredAt == nil || st.ContextJSON == "" {
		t.Fatalf("告警态往返失真: %+v", st)
	}
	// 解除:firing=false + last_fired_at 保留
	if err := UpsertAlertState(db, AlertState{
		Provider: "deepseek", RuleKey: "tier:72", Firing: false, Severity: "warning",
		LastFiredAt: &fired, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("覆盖告警态: %v", err)
	}
	states, _ = ListAlertStates(db, "deepseek")
	if states[0].Firing || states[0].LastFiredAt == nil || states[0].ContextJSON != "" {
		t.Fatalf("解除态失真: %+v", states[0])
	}
}

func TestBalancePrev_平移语义(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "muniu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	c := func(v int64) *int64 { return &v }
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

	// 首次成功写入:无转移可判(prev 为 NULL)
	if err := RecordBalanceSuccess(db, BalanceSnapshot{Provider: "p", KeyID: "k", Source: "deepseek", BalanceCredits: c(100), Currency: "CNY", AsOfAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	rows, err := ListBalanceSnapshots(db, "p")
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %v", err)
	}
	if rows[0].HasPrevTransition() {
		t.Fatalf("首次写入不应有转移: %+v", rows[0])
	}

	// 第二次成功:前值平移为 100,当前 40——转移可判
	at2 := at.Add(time.Hour)
	if err := RecordBalanceSuccess(db, BalanceSnapshot{Provider: "p", KeyID: "k", Source: "deepseek", BalanceCredits: c(40), Currency: "CNY", AsOfAt: at2, UpdatedAt: at2}); err != nil {
		t.Fatal(err)
	}
	rows, _ = ListBalanceSnapshots(db, "p")
	if !rows[0].HasPrevTransition() || *rows[0].PrevBalanceCredits != 100 || *rows[0].BalanceCredits != 40 {
		t.Fatalf("平移不符: %+v", rows[0])
	}
	if !rows[0].PrevAsOfAt.Equal(at) || rows[0].PrevCurrency != "CNY" {
		t.Fatalf("前值时刻/币种不符: %+v", rows[0])
	}

	// 失败查询:前值与当前均不动(失败不产生新转移)
	if err := RecordBalanceFailure(db, "p", "k", "deepseek", "http_status_503", at2.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows, _ = ListBalanceSnapshots(db, "p")
	if !rows[0].HasPrevTransition() || *rows[0].PrevBalanceCredits != 100 || *rows[0].BalanceCredits != 40 {
		t.Fatalf("失败不得改变转移判据: %+v", rows[0])
	}

	// 第三次成功:转移整体前移(prev=40,cur=38)
	at3 := at2.Add(time.Hour)
	if err := RecordBalanceSuccess(db, BalanceSnapshot{Provider: "p", KeyID: "k", Source: "deepseek", BalanceCredits: c(38), Currency: "CNY", AsOfAt: at3, UpdatedAt: at3}); err != nil {
		t.Fatal(err)
	}
	rows, _ = ListBalanceSnapshots(db, "p")
	if *rows[0].PrevBalanceCredits != 40 || *rows[0].BalanceCredits != 38 {
		t.Fatalf("转移应前移: %+v", rows[0])
	}
}

func TestBurnWindowRange_区间过滤与天数计数(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "muniu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	insertUsage := func(day string, credits int64) {
		_, err := db.Exec(`INSERT INTO usage_logs (id, request_id, tenant_id, api_key_id, provider, model_alias, upstream_model, stream, status, latency_total_ms, cost_credits, estimated, created_at)
			VALUES (?, ?, 'default', 'k', 'p', 'm', 'm', 1, 'ok', 1, ?, 0, ?)`,
			day+"-id", day+"-req", credits, day+"T12:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
	}
	// 上一窗口 2 天(+1 天窗口外)、当前窗口 1 天
	insertUsage("2026-10-20", 100)
	insertUsage("2026-10-22", 50)
	insertUsage("2026-10-18", 999) // 区间外
	insertUsage("2026-10-25", 700) // 当前窗口

	// 上一窗口 [10-19, 10-24):命中 20/22 两天,窗口外 18 号不入
	days, err := BurnWindowRange(db, "p", "2026-10-19T00:00:00Z", "2026-10-24T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Day != "2026-10-20" || days[0].Credits != 100 || days[1].Credits != 50 {
		t.Fatalf("上一窗口聚合不符: %+v", days)
	}
	// 无上界形态(既有 BurnWindow 语义)不受影响
	all, err := BurnWindow(db, "p", "2026-10-19T00:00:00Z")
	if err != nil || len(all) != 3 {
		t.Fatalf("无上界聚合不符: %+v err=%v", all, err)
	}
}
