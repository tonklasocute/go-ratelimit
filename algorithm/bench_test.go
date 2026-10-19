package algorithm

import (
	"testing"
	"time"
)

// BenchmarkTake measures the pure algorithm step, without a store.
func BenchmarkTake(b *testing.B) {
	for _, id := range All {
		p := Params{Limit: 1 << 30, Period: time.Second, Burst: 1 << 30}
		if id == SlidingLog {
			p = Params{Limit: 1000, Period: time.Second, Burst: 1000} // memory grows with the limit
		}
		b.Run(id.String(), func(b *testing.B) {
			b.ReportAllocs()
			var st State
			now := t0
			for i := 0; i < b.N; i++ {
				now += 1000
				Take(id, &st, p, now, 1)
			}
		})
	}
}
