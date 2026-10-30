package balance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/solvela/muniu/internal/alerting"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
)

// ChannelKey 渠道内参与余额观测的 key 形态(装配层从 keypool.Snapshot 映射,
// 本包不依赖 keypool):Bearer 源逐 key 查询,账户级源传单元素哨兵。
type ChannelKey struct {
	ID       string
	Value    string
	Disabled bool
}

// ChannelBinding 渠道余额源运行态绑定(装配层构建,评估时动态读取以感知拓扑热替换)。
type ChannelBinding struct {
	Kind         string
	Querier      Querier      // none/manual 为 nil
	Keys         []ChannelKey // Bearer 源=池内全部 key;云账务/无查询源=单元素哨兵
	BelowCredits *int64       // 绝对阈值(credit,余额币种);nil = 未声明
	Currency     string       // manual 源的声明币种
}

// Options 评估循环装配参数;零值经 NewService 兜底缺省。
type Options struct {
	DB               *sql.DB
	Notifier         *alerting.Notifier // nil = 仅落库与状态机,不外发
	Logger           *slog.Logger
	Interval         time.Duration // 评估间隔,缺省 1h
	PollTimeout      time.Duration // 单次上游查询超时,缺省 10s
	WindowDays       int           // 燃速回看窗口,缺省 7
	MinSampleDays    int           // 缺省 2
	TierHours        []int64       // 缺省 30/14/7/3/1 天
	RearmMilli       int64         // 缺省 1200
	FailureThreshold int           // 缺省 3
	// SpikeRatioMilli 燃速突变触发线(毫单位,当前/上一窗口日均比值);0 = 禁用。
	// 缺省值由配置层兜底(nil→5000),此处 0 视为显式禁用,不再二次兜底。
	SpikeRatioMilli int64
	// DropRatioMilli 余额骤降触发线(毫单位,相邻成功快照跌幅,仅 Bearer 源);0 = 禁用。
	DropRatioMilli int64
	Settlement     string // CNY|USD,缺省 CNY
	Now            func() time.Time
	// Sources 每次评估动态取渠道绑定(感知拓扑热替换):渠道名 → 绑定。
	Sources func() map[string]ChannelBinding
}

// Service 余额评估循环:启动即评一轮 → 间隔评估 → 单渠道 refresh 信号合流;
// 评估异步于数据面,单渠道 panic 隔离;Close 优雅停止(取消在途、状态已随评估落库)。
type Service struct {
	opts     Options
	log      *slog.Logger
	ctx      context.Context
	cancel   context.CancelFunc
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	refresh  chan string
}

// NewService 构造并兜底缺省;调用方随后 Start()。
func NewService(opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Hour
	}
	if opts.PollTimeout <= 0 {
		opts.PollTimeout = 10 * time.Second
	}
	if opts.WindowDays <= 0 {
		opts.WindowDays = 7
	}
	if opts.MinSampleDays <= 0 {
		opts.MinSampleDays = 2
	}
	if len(opts.TierHours) == 0 {
		opts.TierHours = []int64{720, 336, 168, 72, 24}
	}
	if opts.RearmMilli <= 0 {
		opts.RearmMilli = 1200
	}
	if opts.FailureThreshold <= 0 {
		opts.FailureThreshold = 3
	}
	if opts.Settlement == "" {
		opts.Settlement = "CNY"
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	opts.Now = now
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		opts: opts, log: opts.Logger,
		ctx: ctx, cancel: cancel,
		stop: make(chan struct{}), done: make(chan struct{}),
		refresh: make(chan string, 16),
	}
}

// Start 启动评估循环(启动即评一轮,不等首个间隔)。
func (s *Service) Start() { go s.loop() }

func (s *Service) loop() {
	defer close(s.done)
	s.evaluateAll()
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.evaluateAll()
		case provider := <-s.refresh:
			s.evaluateProvider(provider)
		}
	}
}

// Close 优雅停止:取消在途查询并等待循环退出;超时兜底防挂死退出路径。
func (s *Service) Close() {
	s.stopOnce.Do(func() {
		close(s.stop)
		s.cancel()
	})
	select {
	case <-s.done:
	case <-time.After(s.opts.PollTimeout + 2*time.Second):
		s.log.Warn("余额评估循环停止超时,继续退出")
	}
}

// RequestRefresh 单渠道立即评估(管理面刷新按钮);队列满则丢弃,下个间隔兜底。
func (s *Service) RequestRefresh(provider string) {
	select {
	case s.refresh <- provider:
	default:
		s.log.Warn("余额刷新队列已满,丢弃本次请求", "provider", provider)
	}
}

func (s *Service) evaluateAll() {
	if s.opts.Sources == nil {
		return
	}
	for name := range s.opts.Sources() {
		s.evaluateProvider(name)
	}
}

// evaluateProvider 单渠道评估:查询 → 落库 → 燃速估算 → 状态机 → 发射;
// panic 按渠道隔离,观测面故障不得拖垮进程。
func (s *Service) evaluateProvider(provider string) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("余额评估 panic(已按渠道隔离)", "provider", provider, "error_detail", fmt.Sprint(r))
		}
	}()
	if s.opts.Sources == nil {
		return
	}
	binding, ok := s.opts.Sources()[provider]
	if !ok {
		return
	}
	now := s.opts.Now()
	// 拓扑热替换移除 key 后先清残留快照行,否则幽灵余额污染 min-估算与失败计数
	keep := make([]string, 0, len(binding.Keys))
	for _, k := range binding.Keys {
		keep = append(keep, k.ID)
	}
	if perr := store.PruneBalanceKeys(s.opts.DB, provider, keep); perr != nil {
		s.log.Error("余额残留 key 清理失败", "provider", provider, "error_detail", perr.Error())
	}
	if binding.Querier != nil {
		s.queryPhase(provider, binding, now)
	}
	s.estimateAndAlert(provider, binding, now)
}

// queryPhase 逐目标查询落库;渠道级失败=全部目标失败(单 key 失败不牵连)。
func (s *Service) queryPhase(provider string, binding ChannelBinding, now time.Time) {
	targets := targetsOf(binding)
	if len(targets) == 0 {
		return // 全部 key 禁用:无查询即无成败信息,不得解除既有 source_error 态
	}
	failedAll := true
	for _, tgt := range targets {
		qctx, cancel := context.WithTimeout(s.ctx, s.opts.PollTimeout)
		bal, err := binding.Querier.Query(qctx, tgt.Value)
		cancel()
		if err != nil {
			class := classifyQueryErr(err)
			s.log.Warn("余额查询失败", "provider", provider, "key_id", tgt.ID,
				"error_class", string(class), "error_detail", err.Error())
			if serr := store.RecordBalanceFailure(s.opts.DB, provider, tgt.ID, binding.Kind, string(class), now); serr != nil {
				s.log.Error("余额失败留痕落库失败", "provider", provider, "error_detail", serr.Error())
			}
			continue
		}
		failedAll = false
		var credits *int64
		if !bal.Unavailable {
			c := bal.Credits
			credits = &c
		}
		if serr := store.RecordBalanceSuccess(s.opts.DB, store.BalanceSnapshot{
			Provider: provider, KeyID: tgt.ID, Source: binding.Kind,
			BalanceCredits: credits, Currency: bal.Currency, AsOfAt: now, UpdatedAt: now,
		}); serr != nil {
			s.log.Error("余额快照落库失败", "provider", provider, "error_detail", serr.Error())
		}
	}
	s.evaluateSourceError(provider, failedAll, now)
}

// targetsOf 查询目标:Bearer 源逐 key(跳过禁用,401 禁用键查询必然失败徒增噪音);
// 云账务源单哨兵。
func targetsOf(binding ChannelBinding) []ChannelKey {
	if IsBearer(binding.Kind) {
		out := make([]ChannelKey, 0, len(binding.Keys))
		for _, k := range binding.Keys {
			if !k.Disabled {
				out = append(out, k)
			}
		}
		return out
	}
	return binding.Keys
}

// evaluateSourceError 连续失败达到阈值单次发运维告警,成功即清零解除;
// 渠道级连续计数 = 各 key 行失败计数的最小值(全部 key 同步失败才推进)。
const ruleKeySourceError = "source_error"

func (s *Service) evaluateSourceError(provider string, failedAll bool, now time.Time) {
	rows, err := store.ListBalanceSnapshots(s.opts.DB, provider)
	if err != nil || len(rows) == 0 {
		return
	}
	minF := rows[0].ConsecutiveFailures
	for _, r := range rows[1:] {
		if r.ConsecutiveFailures < minF {
			minF = r.ConsecutiveFailures
		}
	}
	prev := s.prevStates(provider)
	st := prev[ruleKeySourceError]
	if minF >= int64(s.opts.FailureThreshold) {
		if st.Firing {
			return
		}
		f := Firing{
			RuleKey: ruleKeySourceError, Severity: "warning", At: now,
			Summary: fmt.Sprintf("渠道 %s 余额源连续 %d 次查询失败,余额观测暂停(成功后自动恢复)", provider, minF),
			Context: map[string]any{"provider": provider, "rule": ruleKeySourceError, "consecutive_failures": minF},
		}
		s.emit(EventKey(provider, f.RuleKey), f)
		if serr := store.UpsertAlertState(s.opts.DB, store.AlertState{
			Provider: provider, RuleKey: f.RuleKey, Firing: true, Severity: f.Severity,
			LastFiredAt: &now, ContextJSON: marshalContext(f), UpdatedAt: now,
		}); serr != nil {
			s.log.Error("余额源告警状态落库失败", "provider", provider, "error_detail", serr.Error())
		}
		return
	}
	if st.Firing {
		// 任一 key 成功即清零(minF=0),解除告警态;last_fired_at 保留供运维追溯
		if serr := store.UpsertAlertState(s.opts.DB, store.AlertState{
			Provider: provider, RuleKey: ruleKeySourceError, Firing: false,
			Severity: st.Severity, LastFiredAt: st.LastFiredAt, UpdatedAt: now,
		}); serr != nil {
			s.log.Error("余额源告警解除落库失败", "provider", provider, "error_detail", serr.Error())
		}
	}
}

// observe 单渠道观测合成:逐 key 快照 − 快照后消耗(币种一致才衰减)+ 燃速窗口聚合;
// 估算与总览共用,保证两处口径单源。
func (s *Service) observe(provider string, now time.Time) (Estimate, []KeyBalance) {
	rows, err := store.ListBalanceSnapshots(s.opts.DB, provider)
	if err != nil {
		s.log.Error("余额快照读取失败", "provider", provider, "error_detail", err.Error())
		return Estimate{}, nil
	}
	keys := make([]KeyBalance, 0, len(rows))
	for _, r := range rows {
		kb := KeyBalance{KeyID: r.KeyID, Currency: r.Currency}
		if r.BalanceCredits != nil {
			c := *r.BalanceCredits
			if r.Currency == s.opts.Settlement && !r.AsOfAt.IsZero() {
				if sp, serr := store.SpendSince(s.opts.DB, provider, r.AsOfAt.UTC().Format(time.RFC3339)); serr == nil {
					kb.SpendCredits = sp
				} else {
					s.log.Error("快照后消耗聚合失败", "provider", provider, "error_detail", serr.Error())
				}
			}
			kb.Credits = &c
		}
		keys = append(keys, kb)
	}
	burn, days := s.burnOf(provider, now)
	est := EstimateChannel(EstimateObservation{
		Keys: keys, Settlement: s.opts.Settlement,
		BurnCredits: burn, DaysWithUsage: days,
		WindowDays: s.opts.WindowDays, MinSampleDays: s.opts.MinSampleDays,
	})
	return est, keys
}

// burnOf 燃速窗口聚合:窗口总消耗与「有消耗天数」(SUM>0 的自然日;零值行不计样本)。
func (s *Service) burnOf(provider string, now time.Time) (int64, int) {
	from := now.UTC().AddDate(0, 0, -s.opts.WindowDays).Format(time.RFC3339)
	days, err := store.BurnWindow(s.opts.DB, provider, from)
	if err != nil {
		s.log.Error("燃速窗口聚合失败", "provider", provider, "error_detail", err.Error())
		return 0, 0
	}
	total := int64(0)
	withUsage := 0
	for _, d := range days {
		total += d.Credits
		if d.Credits > 0 {
			withUsage++
		}
	}
	return total, withUsage
}

// spikeRatioOf 燃速突变判据合成:当前窗口日均 ÷ 上一等长窗口日均(毫单位)与基线日均。
// 判据不可得(禁用/零消耗/任一窗口样本不足/零基线)返回 nil,状态机保持原态——
// 基线窗口零消耗=无基线不臆测,与档位估算「数据不足」同一诚实纪律。
func (s *Service) spikeRatioOf(provider string, est Estimate, now time.Time) (*int64, int64) {
	if s.opts.SpikeRatioMilli <= 0 {
		return nil, 0
	}
	if est.BurnPerDay <= 0 || est.DaysWithUsage < s.opts.MinSampleDays {
		return nil, 0
	}
	from := now.UTC().AddDate(0, 0, -2*s.opts.WindowDays).Format(time.RFC3339)
	to := now.UTC().AddDate(0, 0, -s.opts.WindowDays).Format(time.RFC3339)
	days, err := store.BurnWindowRange(s.opts.DB, provider, from, to)
	if err != nil {
		s.log.Error("上一燃速窗口聚合失败", "provider", provider, "error_detail", err.Error())
		return nil, 0
	}
	prevTotal := int64(0)
	prevDays := 0
	for _, d := range days {
		prevTotal += d.Credits
		if d.Credits > 0 {
			prevDays++
		}
	}
	if prevTotal <= 0 || prevDays < s.opts.MinSampleDays {
		return nil, 0
	}
	prevPerDay := prevTotal / int64(s.opts.WindowDays)
	ratio := est.BurnPerDay * 1000 / prevPerDay
	return &ratio, prevPerDay
}

// cliffTransitionsOf 逐 key 相邻转移集(骤降判据):仅 Bearer 查询源参与——manual 人工录入
// 天然跳变、云账务有月账单周期性大跌,均不适用;转移取快照行的 prev_* 平移列,
// 由查询成功写入时平移维护,失败查询不产生新转移。
func (s *Service) cliffTransitionsOf(provider string, binding ChannelBinding) map[string]CliffTransition {
	if s.opts.DropRatioMilli <= 0 || !IsBearer(binding.Kind) {
		return nil
	}
	rows, err := store.ListBalanceSnapshots(s.opts.DB, provider)
	if err != nil {
		s.log.Error("骤降转移快照读取失败", "provider", provider, "error_detail", err.Error())
		return nil
	}
	out := make(map[string]CliffTransition, len(rows))
	for _, r := range rows {
		if r.KeyID == store.AccountKeyID || !r.HasPrevTransition() || r.BalanceCredits == nil {
			continue // 无前值/无现值:转移不可判
		}
		out[r.KeyID] = CliffTransition{
			KeyID:        r.KeyID,
			PrevCredits:  *r.PrevBalanceCredits,
			CurrCredits:  *r.BalanceCredits,
			SameCurrency: r.Currency != "" && r.PrevCurrency == r.Currency,
			Currency:     r.Currency,
		}
	}
	return out
}

// estimateAndAlert 燃速估算 → 状态机 → 全量状态落库 + 发射。
func (s *Service) estimateAndAlert(provider string, binding ChannelBinding, now time.Time) {
	est, _ := s.observe(provider, now)
	prev := s.prevStates(provider)
	cliffTrans := s.cliffTransitionsOf(provider, binding)
	cliffKeys := make([]string, 0, len(cliffTrans))
	for k := range cliffTrans {
		cliffKeys = append(cliffKeys, k)
	}
	sort.Strings(cliffKeys) // 规则集顺序稳定,便于测试与状态表可读
	spike, prevPerDay := s.spikeRatioOf(provider, est, now)
	rules := RulesFor(s.opts.TierHours, binding.BelowCredits, s.opts.SpikeRatioMilli, cliffKeys, s.opts.DropRatioMilli)
	states, firings := Evaluate(EvaluateInput{
		Provider: provider, Rules: rules, Prev: prev, Est: est,
		BelowValue: est.MinEffective, RearmMilli: s.opts.RearmMilli, Now: now,
		SpikeRatioMilli: spike, PrevBurnPerDay: prevPerDay, CurBurnPerDay: est.BurnPerDay,
		CliffTransitions: cliffTrans,
	})
	for ruleKey, st := range states {
		sst := store.AlertState{
			Provider: provider, RuleKey: ruleKey, Firing: st.Firing,
			Severity: st.Severity, ContextJSON: st.ContextJSON, UpdatedAt: now,
		}
		if st.LastFiredAt != nil {
			t := *st.LastFiredAt
			sst.LastFiredAt = &t
		}
		if uerr := store.UpsertAlertState(s.opts.DB, sst); uerr != nil {
			s.log.Error("告警状态落库失败", "provider", provider, "rule", ruleKey, "error_detail", uerr.Error())
		}
	}
	for _, f := range firings {
		s.emit(EventKey(provider, f.RuleKey), f)
	}
}

func (s *Service) emit(eventKey string, f Firing) {
	s.log.Info("余额告警发射", "event", eventKey, "severity", f.Severity)
	s.opts.Notifier.EmitContext(eventKey, f.Severity, f.Summary, f.Context)
}

func (s *Service) prevStates(provider string) map[string]RuleState {
	states, err := store.ListAlertStates(s.opts.DB, provider)
	if err != nil {
		s.log.Error("告警状态读取失败", "provider", provider, "error_detail", err.Error())
		return map[string]RuleState{}
	}
	out := make(map[string]RuleState, len(states))
	for _, st := range states {
		out[st.RuleKey] = RuleState{
			Firing: st.Firing, Severity: st.Severity,
			LastFiredAt: st.LastFiredAt, ContextJSON: st.ContextJSON,
		}
	}
	return out
}

// EventKey 余额观测事件的 webhook 事件键(rule_key 与事件名解耦:tier 档位带小时后缀)。
func EventKey(provider, ruleKey string) string {
	switch {
	case ruleKey == RuleKeyBelow:
		return "balance_below:" + provider
	case ruleKey == ruleKeySourceError:
		return "balance_source_error:" + provider
	case ruleKey == RuleKeyBurnSpike:
		return "balance_burn_spike:" + provider
	case strings.HasPrefix(ruleKey, RuleKeyCliffPrefix):
		return "balance_cliff:" + provider + ":" + strings.TrimPrefix(ruleKey, RuleKeyCliffPrefix)
	case strings.HasPrefix(ruleKey, RuleKeyTierPrefix):
		return "balance_tier:" + provider + ":" + strings.TrimPrefix(ruleKey, RuleKeyTierPrefix) + "h"
	default:
		return "balance_event:" + provider + ":" + ruleKey
	}
}

func classifyQueryErr(err error) Class {
	// 先判链上的截止/取消:网络层错误经 %w 包装,ctx 超时穿透到此处
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassTimeout
	}
	var qe *QueryError
	if errors.As(err, &qe) {
		return qe.Class
	}
	return ClassNetwork
}

// Settlement 结算币种(燃速计价币种,总览透出供前端口径标注)。
func (s *Service) Settlement() string { return s.opts.Settlement }

// HasProvider 报告渠道是否声明了余额源(refresh 端点的存在性校验)。
func (s *Service) HasProvider(provider string) bool {
	if s.opts.Sources == nil {
		return false
	}
	_, ok := s.opts.Sources()[provider]
	return ok
}

// RecordManual 手动录入余额快照(manual 源唯一入口):金额十进制串,币种须与配置声明一致;
// 录入即落库并请求立即评估。未声明余额源的渠道拒绝录入(零声明渠道零落库契约)。
func (s *Service) RecordManual(provider, amount, currency string) error {
	if s.opts.Sources == nil {
		return fmt.Errorf("balance: 余额观测未启用")
	}
	binding, ok := s.opts.Sources()[provider]
	if !ok {
		return fmt.Errorf("balance: 渠道 %s 未声明余额源,不接受录入", provider)
	}
	if binding.Kind != SourceManual {
		return fmt.Errorf("balance: 渠道 %s 余额源为 %s,不接受手动录入", provider, binding.Kind)
	}
	if !strings.EqualFold(currency, binding.Currency) {
		return fmt.Errorf("balance: 渠道 %s 声明币种为 %s,录入币种 %s 不一致", provider, binding.Currency, currency)
	}
	credits, err := metering.ParsePrice(amount)
	if err != nil {
		return fmt.Errorf("balance: 金额非法 %q(须为正数十进制串,≤9 位小数,不接受符号)", amount)
	}
	if credits <= 0 {
		// 主规格 MUST:非正数(含 0)拒绝;负数已被 ParsePrice 的 isDigits 拦截,此处兜 0
		return fmt.Errorf("balance: 金额须为正数,得 %q", amount)
	}
	now := s.opts.Now()
	cur := strings.ToUpper(currency)
	if serr := store.RecordBalanceSuccess(s.opts.DB, store.BalanceSnapshot{
		Provider: provider, KeyID: store.AccountKeyID, Source: SourceManual,
		BalanceCredits: &credits, Currency: cur, AsOfAt: now, UpdatedAt: now,
		ManualAmount: amount,
	}); serr != nil {
		return serr
	}
	s.RequestRefresh(provider)
	return nil
}
