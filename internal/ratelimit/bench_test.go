// BenchmarkRateLimitAllow 测量单 key 的限流判定开销。
package ratelimit

import "testing"

// benchSink 防编译器消除调用的接收变量。
var benchSink bool

func BenchmarkRateLimitAllow(b *testing.B) {
	l := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, _ := l.Allow("bench", 600)
		benchSink = ok
	}
}
