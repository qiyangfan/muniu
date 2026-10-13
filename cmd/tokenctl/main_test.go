package main

import (
	"math"
	"testing"
)

func Test金额展示_credit本币换算(t *testing.T) {
	for _, c := range []struct {
		credits int64
		want    string
	}{
		{0, "0.000000000"},
		{3, "0.000000003"},
		{1_000_000_000, "1.000000000"},
		{100_000_000_000_000, "100000.000000000"},
		{math.MaxInt64, "9223372036.854775807"},
	} {
		if got := formatCredits(c.credits); got != c.want {
			t.Fatalf("credit=%d 显示=%s 期望=%s", c.credits, got, c.want)
		}
	}
}
