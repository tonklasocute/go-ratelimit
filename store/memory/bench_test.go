package memory_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/algorithm"
	"github.com/tonklasocute/go-ratelimit/store/memory"
)

func benchLimiter(b *testing.B, id ratelimit.Algorithm) *ratelimit.RuleLimiter {
	rate := ratelimit.PerSecond(1 << 30) // mostly allowed: measures the full path
	if id == ratelimit.SlidingLog {
		rate = ratelimit.PerSecond(1000) // the log grows with the limit
	}
	s := memory.New()
	b.Cleanup(func() { s.Close() })
	lim, err := ratelimit.New(rate, ratelimit.WithAlgorithm(id), ratelimit.WithStore(s))
	if err != nil {
		b.Fatal(err)
	}
	return lim
}

// BenchmarkMemory measures Allow through the limiter and memory store:
// serial on one key, parallel with a key per goroutine, and parallel on a
// single hot key (worst-case lock contention).
func BenchmarkMemory(b *testing.B) {
	ctx := context.Background()
	for _, id := range algorithm.All {
		b.Run(id.String()+"/serial", func(b *testing.B) {
			lim := benchLimiter(b, id)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				lim.Allow(ctx, "key")
			}
		})
		b.Run(id.String()+"/parallel", func(b *testing.B) {
			lim := benchLimiter(b, id)
			var n atomic.Int64
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				key := "key:" + strconv.FormatInt(n.Add(1), 10)
				for pb.Next() {
					lim.Allow(ctx, key)
				}
			})
		})
		b.Run(id.String()+"/hotkey", func(b *testing.B) {
			lim := benchLimiter(b, id)
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					lim.Allow(ctx, "hot")
				}
			})
		})
	}
}

// BenchmarkMemoryComposite measures two rules applied atomically.
func BenchmarkMemoryComposite(b *testing.B) {
	s := memory.New()
	defer s.Close()
	lim, err := ratelimit.NewComposite([]ratelimit.Rule{
		{Rate: ratelimit.PerSecond(1 << 30)},
		{Rate: ratelimit.PerHour(1 << 30), Algorithm: ratelimit.SlidingWindow},
	}, ratelimit.WithStore(s))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	var n atomic.Int64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		key := "key:" + strconv.FormatInt(n.Add(1), 10)
		for pb.Next() {
			lim.Allow(ctx, key)
		}
	})
}
