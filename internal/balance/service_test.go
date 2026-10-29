package balance

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/alerting"
	"github.com/solvela/muniu/internal/store"
)

// hookCapture webhook 收端:原子收集载荷,轮询断言用。
type hookCapture struct {
	mu      sync.Mutex
	payload []map[string]any
}

func (h *hookCapture) add(v map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.payload = append(h.payload, v)
}

func (h *hookCapture) all() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]map[string]any, len(h.payload))
	copy(out, h.payload)
	return out
}

func (h *hookCapture) eventsOf(prefix string) []map[string]any {
	var out []map[string]any
	for _, p := range h.all() {
		if ev, _ := p["event"].(string); strings.HasPrefix(ev, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// waitFor 轮询至条件成立或超时,超时即 Fatal。
func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", desc)
}

type testEnv struct {
	db     *sql.DB
	svc    *Service
	hooks  *hookCapture
	bindMu sync.Mutex
	bind   map[string]ChannelBinding
	stub   *httptest.Server
	script *stubScript
}

// addBinding 声明渠道绑定:循环 goroutine 会经 Sources 读本表,直写必须持锁。
func (e *testEnv) addBinding(name string, b ChannelBinding) {
	e.bindMu.Lock()
	defer e.bindMu.Unlock()
	e.bind[name] = b
}

// stubScript 可翻面的假上游剧本:响应由互斥锁保护,race 下换剧本无数据竞争。
type stubScript struct {
	mu     sync.Mutex
	status int
	body   string
}

func (s *stubScript) handle(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(s.status)
	_, _ = w.Write([]byte(s.body))
}

func (s *stubScript) flip(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body = status, body
}

// newTestEnv 组装真库 + 假上游 + webhook 收端;stubBody 为空时不建 stub(纯 manual 场景)。
func newTestEnv(t *testing.T, stubStatus int, stubBody string, mutate func(*Options)) *testEnv {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	env := &testEnv{
		db:    db,
		hooks: &hookCapture{},
		bind:  map[string]ChannelBinding{},
	}
	if stubBody != "" || stubStatus != 0 {
		env.script = &stubScript{status: stubStatus, body: stubBody}
		env.stub = httptest.NewServer(http.HandlerFunc(env.script.handle))
		t.Cleanup(env.stub.Close)
	}
	hooksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		env.hooks.add(p)
		w.WriteHeader(200)
	}))
	t.Cleanup(hooksSrv.Close)

	opts := Options{
		DB:          db,
		Notifier:    alerting.New(alerting.Options{WebhookURL: hooksSrv.URL, Logger: testLogger(t)}),
		Logger:      testLogger(t),
		Interval:    40 * time.Millisecond,
		PollTimeout: time.Second,
		Settlement:  "CNY",
		Now:         func() time.Time { return time.Now() },
		Sources: func() map[string]ChannelBinding {
			env.bindMu.Lock()
			defer env.bindMu.Unlock()
			out := map[string]ChannelBinding{}
			for k, v := range env.bind {
				out[k] = v
			}
			return out
		},
	}
	if mutate != nil {
		mutate(&opts)
	}
	env.svc = NewService(opts)
	t.Cleanup(env.svc.Close)
	return env
}

func (e *testEnv) start() { e.svc.Start() }

// seedUsage 插入某渠道窗口内的消耗行(分布在不同自然日,保证最小样本)。
func seedUsage(t *testing.T, env *testEnv, provider string, days int, perDay int64) {
	t.Helper()
	now := time.Now()
	rows := make([]store.UsageRow, 0, days)
	for i := 0; i < days; i++ {
		rows = append(rows, store.UsageRow{
			ID: uuidLike(provider, i), RequestID: "r", TenantID: "t", APIKeyID: "k",
			Provider: provider, ModelAlias: "m", UpstreamModel: "m",
			Status: "ok", CostCredits: perDay, Currency: "CNY",
			CreatedAt: now.AddDate(0, 0, -i).Truncate(time.Hour),
		})
	}
	if err := store.InsertUsageBatch(env.db, rows); err != nil {
		t.Fatalf("种子消耗行: %v", err)
	}
}

func uuidLike(p string, i int) string {
	return p + "-usage-" + time.Now().Format("20060102150405") + "-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for ; i > 0; i /= 10 {
		digits = string(rune('0'+i%10)) + digits
	}
	return digits
}

// testLogger 静默日志(io.Discard),测试输出零噪音。
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mustQuerier 构造指向 stub 的 Bearer 查询器。
func mustQuerier(t *testing.T, kind, baseURL string) Querier {
	t.Helper()
	q, err := For(kind, SourceConfig{BaseURL: baseURL})
	if err != nil || q == nil {
		t.Fatalf("构造查询器 %s: %v", kind, err)
	}
	return q
}

func TestLoop_Bearer启动即评与快照落库(t *testing.T) {
	env := newTestEnv(t, 200, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00"}]}`, nil)
	env.addBinding("deepseek", ChannelBinding{
		Kind:    SourceDeepseek,
		Querier: mustQuerier(t, SourceDeepseek, env.stub.URL),
		Keys:    []ChannelKey{{ID: "DSK", Value: "sk-x"}},
	})
	env.start()
	waitFor(t, "快照落库", func() bool {
		rows, err := store.ListBalanceSnapshots(env.db, "deepseek")
		return err == nil && len(rows) == 1 && rows[0].BalanceCredits != nil && *rows[0].BalanceCredits == 110_000_000_000
	})
	rows, _ := store.ListBalanceSnapshots(env.db, "deepseek")
	if rows[0].Currency != "CNY" || rows[0].Source != SourceDeepseek || rows[0].ConsecutiveFailures != 0 {
		t.Fatalf("快照字段失真: %+v", rows[0])
	}
}

func TestLoop_manual档位告警发射与载荷上下文(t *testing.T) {
	env := newTestEnv(t, 0, "", nil)
	env.addBinding("ch", ChannelBinding{Kind: SourceManual, Currency: "CNY"})
	// 余额 30,日耗 10 → 剩余 72h:168/72 档触发,24 档不触发
	seedUsage(t, env, "ch", 7, 10_000_000_000)
	env.start()
	// 启动即评先跑一轮(此时无快照);手动录入触发 refresh
	waitFor(t, "首轮评估落位", func() bool { time.Sleep(50 * time.Millisecond); return true })
	if err := env.svc.RecordManual("ch", "30", "CNY"); err != nil {
		t.Fatalf("手动录入: %v", err)
	}
	waitFor(t, "档位告警发射", func() bool {
		return len(env.hooks.eventsOf("balance_tier:ch:168h")) >= 1 &&
			len(env.hooks.eventsOf("balance_tier:ch:72h")) >= 1
	})
	if len(env.hooks.eventsOf("balance_tier:ch:24h")) != 0 {
		t.Fatalf("24h 档不应触发: %+v", env.hooks.eventsOf("balance_tier:ch:24h"))
	}
	evt := env.hooks.eventsOf("balance_tier:ch:72h")[0]
	if evt["severity"] != "warning" {
		t.Fatalf("72h 档严重级失真: %+v", evt)
	}
	ctxMap, _ := evt["context"].(map[string]any)
	if ctxMap == nil || ctxMap["provider"] != "ch" || ctxMap["balance"] != "30" {
		t.Fatalf("载荷上下文失真: %+v", evt)
	}
	// 状态持久:firing 态在库
	states, _ := store.ListAlertStates(env.db, "ch")
	firingCount := 0
	for _, st := range states {
		if st.Firing {
			firingCount++
		}
	}
	if firingCount != 4 {
		t.Fatalf("持久化 firing 态失真: %+v", states)
	}
}

func TestLoop_失败容忍单次运维告警与成功解除(t *testing.T) {
	env := newTestEnv(t, 500, `{}`, func(o *Options) { o.FailureThreshold = 2 })
	env.addBinding("volc", ChannelBinding{
		Kind:    SourceVolcBSS,
		Querier: &volcBSSQuerier{accessKey: "ak", secretKey: "sk", endpoint: env.stub.URL},
		Keys:    []ChannelKey{{ID: store.AccountKeyID}},
	})
	env.start()
	waitFor(t, "运维告警单次发射", func() bool {
		return len(env.hooks.eventsOf("balance_source_error:volc")) == 1
	})
	// 继续失败:不重复发射
	time.Sleep(120 * time.Millisecond)
	if n := len(env.hooks.eventsOf("balance_source_error:volc")); n != 1 {
		t.Fatalf("连续失败不得重复发射,实际 %d 次", n)
	}
	states, _ := store.ListAlertStates(env.db, "volc")
	var seFiring bool
	for _, st := range states {
		if st.RuleKey == ruleKeySourceError && st.Firing {
			seFiring = true
		}
	}
	if !seFiring {
		t.Fatalf("source_error 状态应 firing: %+v", states)
	}
	// 上游恢复:计数清零并解除
	env.script.flip(200, `{"Result":{"BalanceList":[{"Balance":"8.00","Currency":"CNY"}]}}`)
	waitFor(t, "恢复后解除", func() bool {
		states, _ := store.ListAlertStates(env.db, "volc")
		for _, st := range states {
			if st.RuleKey == ruleKeySourceError {
				// 解除后 last_fired_at 必须保留(运维可追溯上次触发)
				if st.Firing || st.LastFiredAt == nil {
					return false
				}
			}
		}
		rows, _ := store.ListBalanceSnapshots(env.db, "volc")
		return len(rows) == 1 && rows[0].ConsecutiveFailures == 0 && rows[0].BalanceCredits != nil
	})
}

func TestClose_优雅停止(t *testing.T) {
	env := newTestEnv(t, 0, "", nil)
	env.addBinding("ch", ChannelBinding{Kind: SourceManual, Currency: "CNY"})
	env.start()
	done := make(chan struct{})
	go func() { env.svc.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Close 未在预算内返回")
	}
}

func TestRecordManual_校验路径(t *testing.T) {
	env := newTestEnv(t, 0, "", nil)
	env.addBinding("ch", ChannelBinding{Kind: SourceManual, Currency: "CNY"})
	env.addBinding("ds", ChannelBinding{Kind: SourceDeepseek})
	if err := env.svc.RecordManual("ch", "abc", "CNY"); err == nil {
		t.Fatalf("非法金额应拒绝")
	}
	if err := env.svc.RecordManual("ch", "10", "USD"); err == nil {
		t.Fatalf("币种不一致应拒绝")
	}
	if err := env.svc.RecordManual("ds", "10", "CNY"); err == nil {
		t.Fatalf("非 manual 渠道应拒绝")
	}
	if err := env.svc.RecordManual("ch", "-1", "CNY"); err == nil {
		t.Fatalf("负金额应拒绝")
	}
	if err := env.svc.RecordManual("ch", "0", "CNY"); err == nil {
		t.Fatalf("零金额应拒绝(主规格:非正数 400)")
	}
	if err := env.svc.RecordManual("ch", "12.34", "cny"); err != nil {
		t.Fatalf("小写币种应归一接受: %v", err)
	}
	rows, _ := store.ListBalanceSnapshots(env.db, "ch")
	if len(rows) != 1 || rows[0].BalanceCredits == nil || *rows[0].BalanceCredits != 12_340_000_000 {
		t.Fatalf("录入快照失真: %+v", rows)
	}
	if rows[0].Currency != "CNY" || rows[0].ManualAmount != "12.34" || rows[0].KeyID != store.AccountKeyID {
		t.Fatalf("录入字段失真: %+v", rows[0])
	}
}

func TestOverview_字段形态与降级文案(t *testing.T) {
	env := newTestEnv(t, 0, "", nil)
	env.addBinding("ch", ChannelBinding{Kind: SourceManual, Currency: "CNY", BelowCredits: ptrInt(5_000_000_000)})
	seedUsage(t, env, "ch", 7, 10_000_000_000)
	env.start()
	_ = env.svc.RecordManual("ch", "30", "CNY")
	waitFor(t, "评估完成", func() bool {
		states, _ := store.ListAlertStates(env.db, "ch")
		return len(states) > 0
	})
	ov := env.svc.Overview()
	if len(ov) != 1 {
		t.Fatalf("总览渠道数失真: %+v", ov)
	}
	p := ov[0]
	if p.Provider != "ch" || p.Source != SourceManual || p.Below != "5" {
		t.Fatalf("总览基础字段失真: %+v", p)
	}
	if p.MinEffective == nil || *p.MinEffective != "30" {
		t.Fatalf("有效余额失真: %+v", p.MinEffective)
	}
	if p.BurnPerDayCredits != 10_000_000_000 || p.DaysWithUsage != 7 {
		t.Fatalf("燃速字段失真: %+v", p)
	}
	if p.RemainingHours == nil || *p.RemainingHours != 72 {
		t.Fatalf("剩余小时失真: %+v", p)
	}
	if len(p.Keys) != 1 || p.Keys[0].Balance == nil || *p.Keys[0].Balance != "30" || p.Keys[0].Snapshot == nil {
		t.Fatalf("key 视图失真: %+v", p.Keys)
	}
	firingRules := 0
	for _, a := range p.Alerts {
		if a.Firing {
			firingRules++
		}
	}
	if firingRules != 4 {
		t.Fatalf("告警态视图失真: %+v", p.Alerts)
	}
	// 降级文案原因通道:零消耗渠道
	_ = env.svc.RecordManual("empty", "3", "CNY")
	env.addBinding("empty", ChannelBinding{Kind: SourceManual, Currency: "CNY"})
	env.svc.RequestRefresh("empty")
	waitFor(t, "empty 渠道评估", func() bool {
		for _, p := range env.svc.Overview() {
			if p.Provider == "empty" {
				return p.EstimateReason == ReasonNoUsage
			}
		}
		return false
	})
}

func TestRecordManual_未声明渠道拒绝(t *testing.T) {
	env := newTestEnv(t, 0, "", nil)
	env.bind["ch"] = ChannelBinding{Kind: SourceManual, Currency: "CNY"}
	if err := env.svc.RecordManual("ghost", "12.34", "CNY"); err == nil {
		t.Fatalf("未声明渠道应拒绝录入")
	}
	rows, _ := store.ListBalanceSnapshots(env.db, "ghost")
	if len(rows) != 0 {
		t.Fatalf("未声明渠道不得落库: %+v", rows)
	}
}

func TestLoop_移除key后残留快照被清理(t *testing.T) {
	env := newTestEnv(t, 200, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00"}]}`, nil)
	env.addBinding("dsx", ChannelBinding{
		Kind:    SourceDeepseek,
		Querier: mustQuerier(t, SourceDeepseek, env.stub.URL),
		Keys:    []ChannelKey{{ID: "K1", Value: "sk-a"}}, // K2 已被拓扑移除
	})
	now := time.Now()
	// 预置 K1/K2 两行残留快照(K2 为幽灵)
	c1, c2 := int64(10), int64(500)
	_ = store.RecordBalanceSuccess(env.db, store.BalanceSnapshot{
		Provider: "dsx", KeyID: "K1", Source: SourceDeepseek, BalanceCredits: &c1, Currency: "CNY", AsOfAt: now, UpdatedAt: now,
	})
	_ = store.RecordBalanceSuccess(env.db, store.BalanceSnapshot{
		Provider: "dsx", KeyID: "K2", Source: SourceDeepseek, BalanceCredits: &c2, Currency: "CNY", AsOfAt: now, UpdatedAt: now,
	})
	env.start()
	waitFor(t, "残留行清理且 K1 刷新", func() bool {
		rows, err := store.ListBalanceSnapshots(env.db, "dsx")
		if err != nil || len(rows) != 1 {
			return false
		}
		return rows[0].KeyID == "K1" && rows[0].BalanceCredits != nil && *rows[0].BalanceCredits == 110_000_000_000
	})
}

func TestLoop_失败占位行总览零值时刻守卫(t *testing.T) {
	env := newTestEnv(t, 500, `{}`, func(o *Options) { o.FailureThreshold = 99 })
	env.bind["volc"] = ChannelBinding{
		Kind:    SourceVolcBSS,
		Querier: &volcBSSQuerier{accessKey: "ak", secretKey: "sk", endpoint: env.stub.URL},
		Keys:    []ChannelKey{{ID: store.AccountKeyID}},
	}
	env.start()
	waitFor(t, "失败占位行落库", func() bool {
		rows, err := store.ListBalanceSnapshots(env.db, "volc")
		return err == nil && len(rows) == 1 && rows[0].BalanceCredits == nil
	})
	for _, p := range env.svc.Overview() {
		if p.Provider != "volc" {
			continue
		}
		if len(p.Keys) != 1 {
			t.Fatalf("key 视图缺失: %+v", p)
		}
		if p.Keys[0].AsOfAt != "" {
			t.Fatalf("失败占位行不得透出零值时刻: %q", p.Keys[0].AsOfAt)
		}
		if p.Keys[0].Balance != nil {
			t.Fatalf("占位行余额应为 null: %+v", p.Keys[0])
		}
		return
	}
	t.Fatalf("总览缺 volc 渠道")
}
