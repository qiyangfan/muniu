package balance

import (
	"sort"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// KeyOverview 渠道内单 key 的余额观测视图(管理面/控制台消费)。
// Balance 为有效余额(快照−衰减)十进制串:null = 不可知;Snapshot 为快照余额的
// credit 归一化十进制串(非录入原文,录入原文落 manual_amount 列不透出)。
type KeyOverview struct {
	KeyID string `json:"key_id"`
	// Balance 为有效余额(快照−衰减)十进制串;Snapshot 为快照余额的 credit 归一化
	// 十进制串(非录入原文)。两者 nil = 余额不可知。
	Balance  *string `json:"balance"`
	Snapshot *string `json:"snapshot"`
	Currency string  `json:"currency"`
	// AsOfAt 快照观测时刻(RFC3339);空串 = 尚未成功查询(失败占位行)。
	AsOfAt              string `json:"as_of_at"`
	ConsecutiveFailures int64  `json:"consecutive_failures"`
	LastError           string `json:"last_error"`
}

// AlertOverview 单规则告警态视图。
type AlertOverview struct {
	RuleKey     string  `json:"rule_key"`
	Firing      bool    `json:"firing"`
	Severity    string  `json:"severity"`
	LastFiredAt *string `json:"last_fired_at"`
}

// ProviderOverview 单渠道总览;金额一律十进制串(余额可能超 JS 安全整数域),
// 64 位量数(燃速/剩余小时)按约定出 number。
type ProviderOverview struct {
	Provider string `json:"provider"`
	Source   string `json:"source"`
	Below    string `json:"below"`
	Currency string `json:"currency"`
	// ManualCurrency manual 源的声明币种(录入校验基准);查询源为空(币种由上游响应决定)。
	ManualCurrency    string          `json:"manual_currency"`
	Keys              []KeyOverview   `json:"keys"`
	MinEffective      *string         `json:"min_effective"`
	BurnPerDayCredits int64           `json:"burn_per_day_credits"`
	WindowDays        int             `json:"burn_window_days"`
	DaysWithUsage     int             `json:"days_with_usage"`
	RemainingHours    *int64          `json:"remaining_hours"`
	EstimateReason    string          `json:"estimate_reason"`
	Alerts            []AlertOverview `json:"alerts"`
	LastCheckedAt     string          `json:"last_checked_at"`
}

// Overview 全渠道观测总览:声明了余额源的渠道恒出现(未查询过也给出空态),
// 估算口径与评估循环单源(同走 observe)。
func (s *Service) Overview() []ProviderOverview {
	out := []ProviderOverview{}
	if s.opts.Sources == nil {
		return out
	}
	bindings := s.opts.Sources()
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)

	rowsBy, statesBy := s.overviewRowsByProvider()
	now := s.opts.Now()
	for _, name := range names {
		out = append(out, s.overviewOf(name, bindings[name], rowsBy, statesBy, now))
	}
	return out
}

// overviewRowsByProvider 一次读全量快照与告警态并按渠道分桶(逐渠道查询会放大读库次数)。
func (s *Service) overviewRowsByProvider() (map[string][]store.BalanceSnapshot, map[string][]store.AlertState) {
	rowsAll, rerr := store.ListBalanceSnapshots(s.opts.DB, "")
	if rerr != nil {
		s.log.Error("总览快照读取失败", "error_detail", rerr.Error())
	}
	statesAll, serr := store.ListAlertStates(s.opts.DB, "")
	if serr != nil {
		s.log.Error("总览告警状态读取失败", "error_detail", serr.Error())
	}
	rowsBy := map[string][]store.BalanceSnapshot{}
	for _, r := range rowsAll {
		rowsBy[r.Provider] = append(rowsBy[r.Provider], r)
	}
	statesBy := map[string][]store.AlertState{}
	for _, st := range statesAll {
		statesBy[st.Provider] = append(statesBy[st.Provider], st)
	}
	return rowsBy, statesBy
}

// overviewOf 单渠道总览行。
func (s *Service) overviewOf(name string, binding ChannelBinding,
	rowsBy map[string][]store.BalanceSnapshot, statesBy map[string][]store.AlertState,
	now time.Time) ProviderOverview {
	po := ProviderOverview{
		Provider: name, Source: binding.Kind, ManualCurrency: binding.Currency,
		WindowDays: s.opts.WindowDays,
		Keys:       []KeyOverview{}, Alerts: []AlertOverview{},
	}
	if binding.BelowCredits != nil {
		po.Below = FormatCredits(*binding.BelowCredits)
	}
	est, keys := s.observe(name, now)
	po.Currency = est.PrimaryCurrency
	po.BurnPerDayCredits = est.BurnPerDay
	po.DaysWithUsage = est.DaysWithUsage
	po.RemainingHours = est.RemainingHours
	po.EstimateReason = est.Reason
	if est.MinEffective != nil {
		v := FormatCredits(*est.MinEffective)
		po.MinEffective = &v
	}
	rows := rowsBy[name]
	po.LastCheckedAt = lastCheckedText(rows)
	rowsByID := make(map[string]store.BalanceSnapshot, len(rows))
	for _, r := range rows {
		rowsByID[r.KeyID] = r
	}
	for _, k := range keys {
		po.Keys = append(po.Keys, keyOverviewOf(k, rowsByID[k.KeyID]))
	}
	for _, st := range statesBy[name] {
		po.Alerts = append(po.Alerts, alertOverviewOf(st))
	}
	return po
}

// lastCheckedText 渠道最近一次观测时刻(零值不出 RFC3339 伪时刻)。
func lastCheckedText(rows []store.BalanceSnapshot) string {
	var lastChecked time.Time
	for _, r := range rows {
		if r.UpdatedAt.After(lastChecked) {
			lastChecked = r.UpdatedAt
		}
	}
	if lastChecked.IsZero() {
		return ""
	}
	return lastChecked.UTC().Format(time.RFC3339)
}

// keyOverviewOf 单 key 视图:有效余额 = 快照 − 快照后消耗(credits 不可知即两者皆空)。
func keyOverviewOf(k KeyBalance, row store.BalanceSnapshot) KeyOverview {
	asOf := "" // 失败占位行 AsOfAt 为零值,透出会被误读为真实观测时刻
	if !row.AsOfAt.IsZero() {
		asOf = row.AsOfAt.UTC().Format(time.RFC3339)
	}
	kov := KeyOverview{
		KeyID:               k.KeyID,
		Currency:            k.Currency,
		AsOfAt:              asOf,
		ConsecutiveFailures: row.ConsecutiveFailures,
		LastError:           row.LastError,
	}
	if k.Credits != nil {
		snap := FormatCredits(*k.Credits)
		kov.Snapshot = &snap
		eff := *k.Credits - k.SpendCredits
		v := FormatCredits(eff)
		kov.Balance = &v
	}
	return kov
}

// alertOverviewOf 单规则告警态视图。
func alertOverviewOf(st store.AlertState) AlertOverview {
	ao := AlertOverview{RuleKey: st.RuleKey, Firing: st.Firing, Severity: st.Severity}
	if st.LastFiredAt != nil {
		v := st.LastFiredAt.UTC().Format(time.RFC3339)
		ao.LastFiredAt = &v
	}
	return ao
}
