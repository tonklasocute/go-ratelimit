package memory_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/ratelimittest"
	"github.com/tonklasocute/go-ratelimit/store/memory"
)

func TestConformance(t *testing.T) {
	ratelimittest.TestStore(t, func(t *testing.T) ratelimit.Store {
		s := memory.New()
		t.Cleanup(func() { s.Close() })
		return s
	})
}

func TestConformanceSingleShard(t *testing.T) {
	ratelimittest.TestStore(t, func(t *testing.T) ratelimit.Store {
		return memory.New(memory.WithShards(1), memory.WithCleanupInterval(0))
	})
}

func newLimiter(t *testing.T, s *memory.Store, clk ratelimit.Clock, opts ...ratelimit.Option) *ratelimit.RuleLimiter {
	t.Helper()
	lim, err := ratelimit.New(append([]ratelimit.Option{ratelimit.WithStore(s), ratelimit.WithClock(clk)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return lim
}

func TestMaxKeysEvictsLeastRecentlyUsed(t *testing.T) {
	s := memory.New(memory.WithShards(1), memory.WithMaxKeys(3), memory.WithCleanupInterval(0))
	clk := ratelimittest.NewClock()
	lim := newLimiter(t, s, clk, ratelimit.PerHour(1))
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c"} {
		lim.Allow(ctx, k)
	}
	lim.Allow(ctx, "a") // a is now the most recently used; b is the oldest
	lim.Allow(ctx, "d") // evicts b
	if got := s.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}
	if res, _ := lim.Allow(ctx, "b"); !res.Allowed {
		t.Fatal("b should have been evicted and start fresh")
	}
	// Adding b evicted c; a and d are still limited.
	for _, k := range []string{"a", "d"} {
		if res, _ := lim.Allow(ctx, k); res.Allowed {
			t.Fatalf("%s should still be limited", k)
		}
	}
}

func TestMaxKeysReducesShards(t *testing.T) {
	s := memory.New(memory.WithShards(64), memory.WithMaxKeys(4), memory.WithCleanupInterval(0))
	lim := newLimiter(t, s, ratelimittest.NewClock(), ratelimit.PerHour(1))
	for i := 0; i < 100; i++ {
		lim.Allow(context.Background(), fmt.Sprint("key", i))
	}
	if got := s.Len(); got > 4 {
		t.Fatalf("Len() = %d, want at most 4", got)
	}
}

func TestDeleteExpired(t *testing.T) {
	clk := ratelimittest.NewClock()
	s := memory.New(memory.WithClock(clk), memory.WithCleanupInterval(0))
	fast := newLimiter(t, s, clk, ratelimit.PerSecond(10))
	slow := newLimiter(t, s, clk, ratelimit.PerMinute(10), ratelimit.WithAlgorithm(ratelimit.FixedWindow))
	ctx := context.Background()
	fast.Allow(ctx, "fast")
	slow.Allow(ctx, "slow")
	fast.Allow(ctx, "both")
	slow.Allow(ctx, "both")

	if n := s.DeleteExpired(); n != 0 {
		t.Fatalf("deleted %d keys right away", n)
	}
	clk.Advance(time.Second)
	if n := s.DeleteExpired(); n != 1 || s.Len() != 2 {
		t.Fatalf("after 1s deleted %d, Len %d; want 1 and 2", n, s.Len())
	}
	clk.Advance(time.Minute)
	if n := s.DeleteExpired(); n != 2 || s.Len() != 0 {
		t.Fatalf("after 1m deleted %d, Len %d; want 2 and 0", n, s.Len())
	}
}

func TestCleanupGoroutine(t *testing.T) {
	clk := ratelimittest.NewClock()
	s := memory.New(memory.WithClock(clk), memory.WithCleanupInterval(time.Millisecond))
	lim := newLimiter(t, s, clk, ratelimit.PerSecond(10))
	lim.Allow(context.Background(), "k")
	clk.Advance(time.Hour)
	deadline := time.Now().Add(5 * time.Second)
	for s.Len() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("cleanup goroutine did not delete the expired key")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
	// Still usable after Close.
	if res, err := lim.Allow(context.Background(), "k"); err != nil || !res.Allowed {
		t.Fatalf("Allow after Close: %+v, %v", res, err)
	}
}

func TestResetKeepsOtherRules(t *testing.T) {
	s := memory.New(memory.WithCleanupInterval(0))
	clk := ratelimittest.NewClock()
	a := newLimiter(t, s, clk, ratelimit.PerHour(1), ratelimit.WithName("a"))
	ctx := context.Background()
	a.Allow(ctx, "k")
	if err := a.Reset(ctx, "k"); err != nil || s.Len() != 0 {
		t.Fatalf("Reset: err %v, Len %d", err, s.Len())
	}
	ctxDone, cancel := context.WithCancel(ctx)
	cancel()
	if err := a.Reset(ctxDone, "k"); err == nil {
		t.Fatal("Reset with canceled context succeeded")
	}
}

func TestManyKeysConcurrently(t *testing.T) {
	s := memory.New(memory.WithCleanupInterval(time.Millisecond), memory.WithMaxKeys(500))
	defer s.Close()
	lim, err := ratelimit.New(ratelimit.PerSecond(100), ratelimit.WithStore(s),
		ratelimit.WithRules(ratelimit.Rule{Rate: ratelimit.PerMinute(1000), Algorithm: ratelimit.SlidingLog}))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				if _, err := lim.Allow(context.Background(), fmt.Sprint("k", (w*7919+i)%1000)); err != nil {
					t.Error(err)
					return
				}
				if i%500 == 0 {
					lim.Reset(context.Background(), "k1")
					s.Len()
				}
			}
		}(w)
	}
	wg.Wait()
	if s.Len() > 500+64 {
		t.Fatalf("Len() = %d exceeds the cap", s.Len())
	}
}
