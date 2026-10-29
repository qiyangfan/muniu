package balance

import (
	"math"
	"testing"
)

func ptrInt(v int64) *int64 { return &v }

func TestEstimate_正常外推与衰减(t *testing.T) {
	// 有效余额 = 快照 − 快照后消耗;45 CNY 燃速 9 CNY/天 → 120 小时
	obs := EstimateObservation{
		Keys:          []KeyBalance{{KeyID: "K", Credits: ptrInt(45_000_000_000), Currency: "CNY", SpendCredits: 0}},
		Settlement:    "CNY",
		BurnCredits:   63_000_000_000,
		DaysWithUsage: 7,
		WindowDays:    7,
		MinSampleDays: 2,
	}
	est := EstimateChannel(obs)
	if est.MinEffective == nil || *est.MinEffective != 45_000_000_000 {
		t.Fatalf("有效余额失真: %+v", est)
	}
	if est.BurnPerDay != 9_000_000_000 {
		t.Fatalf("日均燃速失真: %d", est.BurnPerDay)
	}
	if est.RemainingHours == nil || *est.RemainingHours != 120 {
		t.Fatalf("剩余小时失真: %+v", est)
	}
	if est.Reason != "" {
		t.Fatalf("不应有降级原因: %q", est.Reason)
	}

	// 快照后消耗实时衰减:100 − 3.5 = 96.5
	obs.Keys = []KeyBalance{{KeyID: "K", Credits: ptrInt(100_000_000_000), Currency: "CNY", SpendCredits: 3_500_000_000}}
	est = EstimateChannel(obs)
	if est.MinEffective == nil || *est.MinEffective != 96_500_000_000 {
		t.Fatalf("快照后衰减失真: %+v", est)
	}
}

func TestEstimate_多key取最小保守(t *testing.T) {
	obs := EstimateObservation{
		Keys: []KeyBalance{
			{KeyID: "A", Credits: ptrInt(80_000_000_000), Currency: "CNY", SpendCredits: 0},
			{KeyID: "B", Credits: ptrInt(30_000_000_000), Currency: "CNY", SpendCredits: 0},
		},
		Settlement:    "CNY",
		BurnCredits:   70_000_000_000, // 7 天窗口合计 70 CNY → 日均 10 CNY
		DaysWithUsage: 7,
		WindowDays:    7,
		MinSampleDays: 2,
	}
	est := EstimateChannel(obs)
	if est.MinEffective == nil || *est.MinEffective != 30_000_000_000 {
		t.Fatalf("应取池内最小余额: %+v", est)
	}
	if est.RemainingHours == nil || *est.RemainingHours != 72 {
		t.Fatalf("保守估算失真(30/10=3 天): %+v", est)
	}
}

func TestEstimate_降级路径表(t *testing.T) {
	cases := []struct {
		name   string
		obs    EstimateObservation
		reason string
	}{
		{"零消耗", EstimateObservation{
			Keys:       []KeyBalance{{KeyID: "K", Credits: ptrInt(5_000_000_000), Currency: "CNY"}},
			Settlement: "CNY", WindowDays: 7, MinSampleDays: 2,
		}, ReasonNoUsage},
		{"样本不足", EstimateObservation{
			Keys:          []KeyBalance{{KeyID: "K", Credits: ptrInt(5_000_000_000), Currency: "CNY"}},
			Settlement:    "CNY",
			BurnCredits:   100,
			DaysWithUsage: 1,
			WindowDays:    7, MinSampleDays: 2,
		}, ReasonInsufficientData},
		{"币种不一致", EstimateObservation{
			Keys:          []KeyBalance{{KeyID: "K", Credits: ptrInt(50_000_000_000), Currency: "USD"}},
			Settlement:    "CNY",
			BurnCredits:   9_000_000_000,
			DaysWithUsage: 7,
			WindowDays:    7, MinSampleDays: 2,
		}, ReasonCurrencyMismatch},
		{"全部不可知", EstimateObservation{
			Keys:          []KeyBalance{{KeyID: "K", Credits: nil, Currency: "USD"}},
			Settlement:    "USD",
			BurnCredits:   9_000_000_000,
			DaysWithUsage: 7,
			WindowDays:    7, MinSampleDays: 2,
		}, ReasonUnknownBalance},
		{"余额钳制护栏", EstimateObservation{
			Keys:          []KeyBalance{{KeyID: "K", Credits: ptrInt(math.MaxInt64), Currency: "CNY"}},
			Settlement:    "CNY",
			BurnCredits:   7_000_000_000, // 日均 1 CNY,BurnPerDay>0 才能走到钳制分支
			DaysWithUsage: 7,
			WindowDays:    7, MinSampleDays: 2,
		}, ReasonOverflow}, // 钳制分支显式 reason,不留「空+nil」第三态
	}
	for _, c := range cases {
		est := EstimateChannel(c.obs)
		if est.Reason != c.reason {
			t.Fatalf("%s: reason = %q, 期望 %q (+%+v)", c.name, est.Reason, c.reason, est)
		}
		if c.reason != "" && est.RemainingHours != nil {
			t.Fatalf("%s: 降级时不得有估算", c.name)
		}
	}
}

func TestEstimate_币种不匹配时余额原值与min跨币种(t *testing.T) {
	// USD 余额 + CNY 结算:不衰减(spend 不得扣 USD),估算禁用,余额仍可用于展示/阈值
	obs := EstimateObservation{
		Keys:          []KeyBalance{{KeyID: "K", Credits: ptrInt(100_000_000_000), Currency: "USD", SpendCredits: 7_000_000_000}},
		Settlement:    "CNY",
		BurnCredits:   9_000_000_000,
		DaysWithUsage: 7,
		WindowDays:    7, MinSampleDays: 2,
	}
	est := EstimateChannel(obs)
	if !est.CurrencyMismatch || est.Reason != ReasonCurrencyMismatch {
		t.Fatalf("应判币种不一致: %+v", est)
	}
	if est.MinEffective == nil || *est.MinEffective != 100_000_000_000 {
		t.Fatalf("跨币种不得扣衰减: %+v", est)
	}
}

func TestFormatCredits_零浮点往返(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{12_340_000_000, "12.34"},
		{0, "0"},
		{1_000_000_000, "1"},
		{-500_000_000, "-0.5"},
		{123, "0.000000123"},
		{110_000_000_000, "110"},
	}
	for _, c := range cases {
		if got := FormatCredits(c.in); got != c.want {
			t.Fatalf("FormatCredits(%d) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestHumanRemaining_表述(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{5, "5 小时"},
		{24, "1 天"},
		{72, "3 天"},
		{76, "3 天 4 小时"},
		{-1, "已透支"},
	}
	for _, c := range cases {
		if got := HumanRemaining(c.in); got != c.want {
			t.Fatalf("HumanRemaining(%d) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestEstimate_混合币种below只取主币种(t *testing.T) {
	// CNY key 80(扣衰减 10) + USD key 5000(原值):mismatch 禁估算,但 below 口径
	// 的 min 只在主币种内取——不得混入 USD 原值参与跨币种比较
	obs := EstimateObservation{
		Keys: []KeyBalance{
			{KeyID: "CN", Credits: ptrInt(80_000_000_000), Currency: "CNY", SpendCredits: 10_000_000_000},
			{KeyID: "US", Credits: ptrInt(500_000_000_000), Currency: "USD"},
		},
		Settlement:    "CNY",
		BurnCredits:   7_000_000_000,
		DaysWithUsage: 7,
		WindowDays:    7, MinSampleDays: 2,
	}
	est := EstimateChannel(obs)
	if !est.CurrencyMismatch || est.Reason != ReasonCurrencyMismatch || est.RemainingHours != nil {
		t.Fatalf("混合币种应禁估算: %+v", est)
	}
	if est.MinEffective == nil || *est.MinEffective != 70_000_000_000 {
		t.Fatalf("主币种 below 口径失真(不得混入 USD 值): %+v", est.MinEffective)
	}

	// 反向混合:主币种为 USD(首 key)——below 口径 = USD 原值 min,不吃 CNY 衰减
	obs.Keys = []KeyBalance{
		{KeyID: "US", Credits: ptrInt(500_000_000_000), Currency: "USD"},
		{KeyID: "CN", Credits: ptrInt(80_000_000_000), Currency: "CNY", SpendCredits: 10_000_000_000},
	}
	est = EstimateChannel(obs)
	if est.MinEffective == nil || *est.MinEffective != 500_000_000_000 {
		t.Fatalf("主币种应取 USD 原值(不衰减不混入 CNY): %+v", est.MinEffective)
	}
}
