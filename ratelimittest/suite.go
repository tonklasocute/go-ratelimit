package ratelimittest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/algorithm"
)

// TestStore runs a conformance suite against a ratelimit.Store. newStore must
// return an empty store that takes its time from the now argument of Take
// (for Redis, use client time). Every subtest uses a fresh store.
func TestStore(t *testing.T, newStore func(t *testing.T) ratelimit.Store) {
	t.Run("Algorithms", func(t *testing.T) { testAlgorithms(t, newStore) })
	t.Run("KeysAreIndependent", func(t *testing.T) { testKeysIndependent(t, newStore) })
	t.Run("CompositeIsAtomic", func(t *testing.T) { testCompositeAtomic(t, newStore) })
	t.Run("PeekDoesNotConsume", func(t *testing.T) { testPeek(t, newStore) })
	t.Run("Reset", func(t *testing.T) { testReset(t, newStore) })
	t.Run("Concurrent", func(t *testing.T) { testConcurrent(t, newStore) })
	t.Run("CanceledContext", func(t *testing.T) { testCanceled(t, newStore) })
}

func newLimiter(t *testing.T, store ratelimit.Store, clk *Clock, opts ...ratelimit.Option) *ratelimit.RuleLimiter {
	t.Helper()
	lim, err := ratelimit.New(append([]ratelimit.Option{ratelimit.WithStore(store), ratelimit.WithClock(clk)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return lim
}

func allow(t *testing.T, lim ratelimit.Limiter, key string, n int) ratelimit.Result {
	t.Helper()
	res, err := lim.AllowN(context.Background(), key, n)
	if err != nil {
		t.Fatalf("AllowN(%q, %d): %v", key, n, err)
	}
	return res
}

func testAlgorithms(t *testing.T, newStore func(t *testing.T) ratelimit.Store) {
	for _, id := range algorithm.All {
		t.Run(id.String(), func(t *testing.T) {
			clk := NewClock()
			lim := newLimiter(t, newStore(t), clk, ratelimit.PerSecond(3), ratelimit.WithAlgorithm(id))
			for i := 0; i < 3; i++ {
				res := allow(t, lim, "k", 1)
				if !res.Allowed || res.Remaining != 2-i || res.Limit != 3 {
					t.Fatalf("request %d: %+v", i, res)
				}
				if res.Policy != id.String()+"_3_1s" {
					t.Fatalf("policy = %q", res.Policy)
				}
			}
			res := allow(t, lim, "k", 1)
			if res.Allowed || res.RetryAfter <= 0 || res.RetryAfter > 2*time.Second {
				t.Fatalf("4th request: %+v", res)
			}
			clk.Advance(res.RetryAfter)
			if res := allow(t, lim, "k", 1); !res.Allowed {
				t.Fatalf("after RetryAfter: %+v", res)
			}
			clk.Advance(2 * time.Second)
			if res := allow(t, lim, "k", 3); !res.Allowed {
				t.Fatalf("after idle: %+v", res)
			}
		})
	}
}

func testKeysIndependent(t *testing.T, newStore func(t *testing.T) ratelimit.Store) {
	clk := NewClock()
	lim := newLimiter(t, newStore(t), clk, ratelimit.PerMinute(1))
	if !allow(t, lim, "a", 1).Allowed || !allow(t, lim, "b", 1).Allowed {
		t.Fatal("first request per key must be allowed")
	}
	if allow(t, lim, "a", 1).Allowed {
		t.Fatal("second request for a must be denied")
	}
}

func testCompositeAtomic(t *testing.T, newStore func(t *testing.T) ratelimit.Store) {
	clk := NewClock()
	lim := newLimiter(t, newStore(t), clk, ratelimit.WithRules(
		ratelimit.Rule{Name: "short", Rate: ratelimit.PerSecond(2)},
		ratelimit.Rule{Name: "long", Rate: ratelimit.PerMinute(3), Algorithm: ratelimit.SlidingLog},
	))
	allow(t, lim, "k", 1)
	if res := allow(t, lim, "k", 1); !res.Allowed || res.Policy != "short" || res.Remaining != 0 {
		t.Fatalf("2nd: %+v", res)
	}
	// Denied by "short": "long" must not be charged.
	if res := allow(t, lim, "k", 1); res.Allowed || res.Policy != "short" {
		t.Fatalf("3rd: %+v", res)
	}
	clk.Advance(time.Second)
	if res := allow(t, lim, "k", 1); !res.Allowed || res.Policy != "long" || res.Remaining != 0 {
		t.Fatalf("after 1s: %+v (long should have exactly 1 left before this)", res)
	}
	clk.Advance(time.Second)
	res := allow(t, lim, "k", 1)
	if res.Allowed || res.Policy != "long" || res.RetryAfter != 58*time.Second {
		t.Fatalf("after 2s: %+v", res)
	}
}

func testPeek(t *testing.T, newStore func(t *testing.T) ratelimit.Store) {
	for _, id := range algorithm.All {
		t.Run(id.String(), func(t *testing.T) {
			lim := newLimiter(t, newStore(t), NewClock(), ratelimit.PerSecond(2), ratelimit.WithAlgorithm(id))
			if res := allow(t, lim, "k", 0); !res.Allowed || res.Remaining != 2 {
				t.Fatalf("peek on empty key: %+v", res)
			}
			allow(t, lim, "k", 1)
			for i := 0; i < 3; i++ {
				if res := allow(t, lim, "k", 0); !res.Allowed || res.Remaining != 1 {
					t.Fatalf("peek %d: %+v", i, res)
				}
			}
		})
	}
}

func testReset(t *testing.T, newStore func(t *testing.T) ratelimit.Store) {
	store := newStore(t)
	clk := NewClock()
	a := newLimiter(t, store, clk, ratelimit.PerMinute(1), ratelimit.WithName("a"))
	b := newLimiter(t, store, clk, ratelimit.PerMinute(1), ratelimit.WithName("b"))
	allow(t, a, "k", 1)
	allow(t, b, "k", 1)
	if err := a.Reset(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if !allow(t, a, "k", 1).Allowed {
		t.Fatal("a must be allowed after Reset")
	}
	if allow(t, b, "k", 1).Allowed {
		t.Fatal("resetting a must not reset b")
	}
	if err := a.Reset(context.Background(), "missing"); err != nil {
		t.Fatalf("Reset of a missing key: %v", err)
	}
}

func testConcurrent(t *testing.T, newStore func(t *testing.T) ratelimit.Store) {
	const limit, workers, perWorker = 50, 10, 20
	for _, id := range algorithm.All {
		t.Run(id.String(), func(t *testing.T) {
			lim := newLimiter(t, newStore(t), NewClock(), ratelimit.PerHour(limit), ratelimit.WithAlgorithm(id))
			var allowed atomic.Int64
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < perWorker; i++ {
						res, err := lim.Allow(context.Background(), "hot")
						if err != nil {
							t.Error(err)
							return
						}
						if res.Allowed {
							allowed.Add(1)
						}
					}
				}()
			}
			wg.Wait()
			if got := allowed.Load(); got != limit {
				t.Fatalf("allowed %d of %d concurrent requests, want exactly %d", got, workers*perWorker, limit)
			}
		})
	}
}

func testCanceled(t *testing.T, newStore func(t *testing.T) ratelimit.Store) {
	store := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rules := []ratelimit.Rule{{Name: "r", Rate: ratelimit.PerSecond(1), Burst: 1, Algorithm: ratelimit.GCRA}}
	if _, err := store.Take(ctx, "k", rules, 1, Epoch); err == nil {
		t.Fatal("Take with a canceled context succeeded")
	}
}
