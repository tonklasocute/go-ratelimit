package redisstore_test

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/ratelimittest"
	"github.com/tonklasocute/go-ratelimit/store/memory"
	redisstore "github.com/tonklasocute/go-ratelimit/store/redis"
)

// These tests run the Lua script on miniredis, an in-process Redis
// implementation with a real Lua interpreter. The integration module runs the
// same suite against real Redis in Docker.

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return mr, rdb
}

func TestConformance(t *testing.T) {
	ratelimittest.TestStore(t, func(t *testing.T) ratelimit.Store {
		_, rdb := newRedis(t)
		return redisstore.New(rdb, redisstore.WithClientTime())
	})
}

func TestServerTime(t *testing.T) {
	mr, rdb := newRedis(t)
	mr.SetTime(ratelimittest.Epoch)
	// The limiter's clock is far in the past; the server's time is used.
	lim, err := ratelimit.New(ratelimit.PerMinute(2), ratelimit.WithAlgorithm(ratelimit.FixedWindow),
		ratelimit.WithStore(redisstore.New(rdb)), ratelimit.WithClock(ratelimittest.NewClockAt(time.Unix(1, 0))))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if res, err := lim.Allow(ctx, "k"); err != nil || !res.Allowed {
			t.Fatalf("request %d: %+v, %v", i, res, err)
		}
	}
	res, err := lim.Allow(ctx, "k")
	if err != nil || res.Allowed || res.RetryAfter != time.Minute {
		t.Fatalf("3rd: %+v, %v (server time is at a window start)", res, err)
	}
	mr.SetTime(ratelimittest.Epoch.Add(time.Minute))
	if res, _ := lim.Allow(ctx, "k"); !res.Allowed {
		t.Fatalf("next window: %+v", res)
	}
}

func TestKeysAndTTL(t *testing.T) {
	mr, rdb := newRedis(t)
	store := redisstore.New(rdb, redisstore.WithPrefix("app:rl:"), redisstore.WithClientTime())
	lim, err := ratelimit.NewComposite([]ratelimit.Rule{
		{Name: "burst", Rate: ratelimit.PerSecond(10)},
		{Name: "log", Rate: ratelimit.PerMinute(5), Algorithm: ratelimit.SlidingLog},
	}, ratelimit.WithStore(store), ratelimit.WithClock(ratelimittest.NewClock()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := lim.AllowN(ctx, "user:42", 2); err != nil {
		t.Fatal(err)
	}
	if got := store.Key("user:42", "burst"); got != "app:rl:{user:42}:burst" {
		t.Fatalf("Key() = %q", got)
	}
	if ttl := mr.TTL("app:rl:{user:42}:burst"); ttl != 200*time.Millisecond {
		t.Fatalf("GCRA TTL = %v, want the time to refill (200ms)", ttl)
	}
	if ttl := mr.TTL("app:rl:{user:42}:log"); ttl != time.Minute {
		t.Fatalf("log TTL = %v", ttl)
	}
	if members, _ := mr.ZMembers("app:rl:{user:42}:log"); len(members) != 2 {
		t.Fatalf("log has %d entries, want 2 distinct members", len(members))
	}
	if err := lim.Reset(ctx, "user:42"); err != nil {
		t.Fatal(err)
	}
	if keys := mr.Keys(); len(keys) != 0 {
		t.Fatalf("keys left after Reset: %v", keys)
	}
	if err := store.Reset(ctx, "user:42", nil); err != nil {
		t.Fatal(err)
	}
}

func TestHashTagEscaping(t *testing.T) {
	s := redisstore.New(nil)
	tests := map[string]string{
		"":    `ratelimit:{\0}:r`,
		"a}b": `ratelimit:{a\]b}:r`,
		`a\]`: `ratelimit:{a\\]}:r`, // "]" is not special; distinct from "a}"
		"{x}": `ratelimit:{{x\]}:r`,
	}
	for key, want := range tests {
		if got := s.Key(key, "r"); got != want {
			t.Errorf("Key(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestReloadsFlushedScript(t *testing.T) {
	_, rdb := newRedis(t)
	lim, err := ratelimit.New(ratelimit.PerSecond(5), ratelimit.WithStore(redisstore.New(rdb)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		rdb.ScriptFlush(ctx) // force NOSCRIPT, then EVAL caches it again
		if res, err := lim.Allow(ctx, "k"); err != nil || !res.Allowed {
			t.Fatalf("attempt %d: %+v, %v", i, res, err)
		}
	}
}

func TestErrors(t *testing.T) {
	mr, rdb := newRedis(t)
	store := redisstore.New(rdb)
	rules := []ratelimit.Rule{{Name: "bad", Rate: ratelimit.PerSecond(1), Burst: 1, Algorithm: 42}}
	if _, err := store.Take(context.Background(), "k", rules, 1, time.Now()); err == nil {
		t.Fatal("unknown algorithm accepted")
	}

	var hooked error
	lim, _ := ratelimit.New(ratelimit.PerSecond(1), ratelimit.WithStore(store),
		ratelimit.WithFailurePolicy(ratelimit.FailClosed),
		ratelimit.WithOnError(func(_ context.Context, _ string, err error) { hooked = err }))
	mr.Close()
	res, err := lim.Allow(context.Background(), "k")
	if err == nil || res.Allowed || hooked == nil {
		t.Fatalf("Redis down: %+v, %v, hook %v", res, err, hooked)
	}
}

// The Lua script must make exactly the decisions of the Go algorithms used
// by the memory store. Times are whole microseconds, Redis' resolution.
func TestMatchesMemoryStore(t *testing.T) {
	for _, alg := range []ratelimit.Algorithm{
		ratelimit.FixedWindow, ratelimit.SlidingLog, ratelimit.SlidingWindow,
		ratelimit.TokenBucket, ratelimit.LeakyBucket, ratelimit.GCRA,
	} {
		t.Run(alg.String(), func(t *testing.T) {
			_, rdb := newRedis(t)
			mem := memory.New(memory.WithCleanupInterval(0))
			red := redisstore.New(rdb, redisstore.WithClientTime())
			rules := []ratelimit.Rule{{Name: "r", Rate: ratelimit.PerSecond(8), Burst: 3, Algorithm: alg}}
			if alg.Windowed() {
				rules[0].Burst = 8
			}
			rng := rand.New(rand.NewSource(int64(alg)))
			ctx := context.Background()
			now := ratelimittest.Epoch
			for i := 0; i < 1500; i++ {
				now = now.Add(time.Duration(rng.Int63n(300_000)) * time.Microsecond)
				n := rng.Intn(rules[0].Capacity() + 1)
				want, err := mem.Take(ctx, "k", rules, n, now)
				if err != nil {
					t.Fatal(err)
				}
				// Redis rounds durations up to whole microseconds.
				want.RetryAfter = ceilMicro(want.RetryAfter)
				want.ResetAfter = ceilMicro(want.ResetAfter)
				got, err := red.Take(ctx, "k", rules, n, now)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("request %d at +%v n=%d:\nredis  %+v\nmemory %+v", i, now.Sub(ratelimittest.Epoch), n, got, want)
				}
			}
		})
	}
}

func ceilMicro(d time.Duration) time.Duration {
	return (d + time.Microsecond - 1) / time.Microsecond * time.Microsecond
}
