// 欠费证据白名单与上报映射契约:证据→欠费桶(封闭白名单,文案不触发),无证据维持既有分类。
package proxy

import (
	"testing"
	"time"

	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
)

func TestIsCreditExhausted_封闭白名单(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"402 状态码直证", 402, `{}`, true},
		{"429+type insufficient_quota", 429, `{"error":{"type":"insufficient_quota","message":" quota"}}`, true},
		{"429+code insufficient_quota", 429, `{"error":{"code":"insufficient_quota"}}`, true},
		{"顶层 code insufficient_quota 变体", 403, `{"code":"insufficient_quota","message":"..."}`, true},
		{"400 余额文案不触发(方言陷阱)", 400, `{"error":{"message":"余额不足,请充值","type":"invalid_request_error"}}`, false},
		{"429 普通限流不触发", 429, `{"error":{"type":"rate_limit_error"}}`, false},
		{"非法 body 不触发", 402 - 400, `not-json`, false},
	}
	for _, tc := range cases {
		if got := isCreditExhausted(tc.status, []byte(tc.body)); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestReportKeyOutcome_欠费证据映射欠费桶(t *testing.T) {
	pool := keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "v", Weight: 1}})
	key := &keypool.Key{ID: "K", Value: "v"}
	p := &Proxy{}
	// reportKeyOutcome 内部取真实时钟,断言用区间容忍调度抖动
	credit := func(remain time.Duration) bool { return remain > 29*time.Minute && remain <= 30*time.Minute }
	rate := func(remain time.Duration) bool { return remain > 59*time.Second && remain <= time.Minute }

	// 证据 + 限流类 → 欠费桶 30m 起步(而非 429 桶 Retry-After 语义)
	p.reportKeyOutcome(pool, key, failover.ClassRateLimited, true,
		&meterState{class: failover.ClassRateLimited, creditEvidence: true})
	if got := pool.CooldownRemain("K", time.Now()); !credit(got) {
		t.Fatalf("欠费证据应进欠费桶(30m 起步): %v", got)
	}

	// 证据 + bad_request 类(非 deepseek 适配器的 402 归类)同样进欠费桶
	pool2 := keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "v", Weight: 1}})
	p.reportKeyOutcome(pool2, key, failover.ClassBadRequest, true,
		&meterState{class: failover.ClassBadRequest, creditEvidence: true})
	if got := pool2.CooldownRemain("K", time.Now()); !credit(got) {
		t.Fatalf("欠费证据应跨错误类映射欠费桶: %v", got)
	}

	// 无证据:维持 429 桶(60s 基数),绝不误入欠费桶
	pool3 := keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "v", Weight: 1}})
	p.reportKeyOutcome(pool3, key, failover.ClassRateLimited, true,
		&meterState{class: failover.ClassRateLimited})
	if got := pool3.CooldownRemain("K", time.Now()); !rate(got) {
		t.Fatalf("无证据应维持 429 桶(60s): %v", got)
	}
}
