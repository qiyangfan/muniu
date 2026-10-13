// 计价基准(phase-2 T2.10):单请求成本换算开销(纯整数运算)。
package metering

import "testing"

var costBenchmarkResult int64

func BenchmarkCost(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		costBenchmarkResult = Cost(1200+int64(i%512), 800, 2_000_000, 8_000_000)
	}
}
