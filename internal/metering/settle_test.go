// 本位币结算测试：同币种透传、USD↔CNY 双向换算、逐段 ceil、
// 缺汇率 fail-fast、非法币种拒绝；换算方向恒定为 1 USD = fx/1000 CNY。
package metering

import "testing"

func TestSettlePrice_同币种透传(t *testing.T) {
	in := Pricing{Currency: "CNY", InputCreditsPerM: 3_000_000, VisionInputCreditsPerM: 2_000_000, OutputCreditsPerM: 9_000_000}
	out, ok := SettlePrice(in, "CNY", 7200)
	if !ok || out != (Pricing{Currency: "CNY", InputCreditsPerM: 3_000_000, VisionInputCreditsPerM: 2_000_000, OutputCreditsPerM: 9_000_000}) {
		t.Fatalf("同币种应原样透传: ok=%v %+v", ok, out)
	}
}

func TestSettlePrice_USD报价转CNY本位(t *testing.T) {
	// $0.003/M → 3e6 credits/M；fx 7.200 → ×7.2 = 21.6e6 → ceil 21_600_000。
	quote := Pricing{Currency: "USD", InputCreditsPerM: 3_000_000, CachedInputCreditsPerM: 300_000,
		VisionInputCreditsPerM: 600_000, OutputCreditsPerM: 9_000_000}
	out, ok := SettlePrice(quote, "CNY", 7200)
	if !ok {
		t.Fatal("应成功")
	}
	if out.Currency != "CNY" {
		t.Fatalf("币种应置为本位币: %+v", out)
	}
	if out.InputCreditsPerM != 21_600_000 {
		t.Fatalf("input 应 ×7.2 = 21_600_000: %d", out.InputCreditsPerM)
	}
	if out.VisionInputCreditsPerM != 4_320_000 {
		t.Fatalf("vision_input 应 ×7.2 = 4_320_000: %d", out.VisionInputCreditsPerM)
	}
	// 逐段 ceil：0.3e6×7.2 = 2_160_000 整除；构造不整除情形。
	out2, _ := SettlePrice(Pricing{Currency: "USD", InputCreditsPerM: 1}, "CNY", 7200)
	if out2.InputCreditsPerM != 8 { // 1×7200/1000 = 7.2 → ceil 8
		t.Fatalf("应逐段 ceil: %d", out2.InputCreditsPerM)
	}
}

func TestSettlePrice_CNY报价转USD本位(t *testing.T) {
	// ¥7.2/M → 7.2e9 credits/M;÷7.2 = 1e9 credits/M
	quote := Pricing{Currency: "CNY", InputCreditsPerM: 7_200_000_000}
	out, ok := SettlePrice(quote, "USD", 7200)
	if !ok || out.Currency != "USD" || out.InputCreditsPerM != 1_000_000_000 {
		t.Fatalf("CNY→USD 应 ÷7.2: ok=%v %+v", ok, out)
	}
}

func TestSettlePrice_缺汇率failFast(t *testing.T) {
	quote := Pricing{Currency: "USD", InputCreditsPerM: 3_000_000}
	if _, ok := SettlePrice(quote, "CNY", 0); ok {
		t.Fatal("异币种且未配汇率应 fail-fast")
	}
	if _, ok := SettlePrice(quote, "CNY", -1); ok {
		t.Fatal("负汇率应 fail-fast")
	}
}

func TestSettlePrice_非法币种拒绝(t *testing.T) {
	if _, ok := SettlePrice(Pricing{Currency: "EUR", InputCreditsPerM: 1}, "CNY", 7200); ok {
		t.Fatal("EUR 报价应拒绝(本位币仅 CNY|USD)")
	}
	if _, ok := SettlePrice(Pricing{Currency: "CNY"}, "EUR", 7200); ok {
		t.Fatal("EUR 本位币应拒绝")
	}
}

func TestSettlePrice_乘法溢出failFast(t *testing.T) {
	// 任一价格换算溢出时，整张价目都应被拒绝。
	huge := Pricing{Currency: "USD", InputCreditsPerM: 9_000_000_000_000_000_000}
	if _, ok := SettlePrice(huge, "CNY", 7200); ok {
		t.Fatal("乘积溢出应 fail-fast")
	}
}
