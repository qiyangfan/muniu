package metering

import (
	"math"
	"testing"
)

func TestImagePrice_精确成本与溢出(t *testing.T) {
	p := ImagePrice{Currency: "CNY", DefaultCredits: 350, Tiers: map[string]int64{"1024|hd": 300}}
	for _, tc := range []struct {
		k    string
		n    int64
		want int64
	}{{"1024|hd", 2, 600}, {"1024|standard", 2, 700}, {"|", 1, 350}} {
		got, err := p.Cost(tc.k, tc.n)
		if err != nil || got != tc.want {
			t.Fatalf("cost=%d err=%v", got, err)
		}
	}
	p.DefaultCredits = math.MaxInt64
	if _, err := p.Cost("|", 2); err == nil {
		t.Fatal("成本溢出未拒")
	}
	if err := ValidateImagePrice(ImagePrice{Currency: "CNY", DefaultCredits: 1, Tiers: map[string]int64{"bad": 1}}); err == nil {
		t.Fatal("非法档位未拒")
	}
}
