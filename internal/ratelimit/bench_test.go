// 限流基准(phase-2 T2.10):单 key 高频 Allow 判定开销。
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
