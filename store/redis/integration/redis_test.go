package integration

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/ratelimittest"
	redisstore "github.com/tonklasocute/go-ratelimit/store/redis"
)

var (
	setupOnce sync.Once
	addr      string
	setupErr  error
	container *tcredis.RedisContainer
)

func TestMain(m *testing.M) {
	code := m.Run()
	if container != nil {
		container.Terminate(context.Background())
	}
	os.Exit(code)
}

// redisAddr returns the address of a Redis server, starting a container on
// first use, or skips the test.
func redisAddr(tb testing.TB) string {
	tb.Helper()
	setupOnce.Do(func() {
		if addr = os.Getenv("REDIS_ADDR"); addr != "" {
			return
		}
		defer func() {
			if r := recover(); r != nil { // testcontainers panics without Docker
				setupErr = fmt.Errorf("docker unavailable: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		container, setupErr = tcredis.Run(ctx, "redis:7-alpine")
		if setupErr != nil {
			return
		}
		var uri string
		if uri, setupErr = container.ConnectionString(ctx); setupErr == nil {
			var opts *redis.Options
			if opts, setupErr = redis.ParseURL(uri); setupErr == nil {
				addr = opts.Addr
			}
		}
	})
	if addr == "" {
		tb.Skipf("no Redis: set REDIS_ADDR or start Docker (%v)", setupErr)
	}
	return addr
}

// newClient returns a client on a fresh database state.
func newClient(tb testing.TB) *redis.Client {
	tb.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr(tb)})
	tb.Cleanup(func() { rdb.Close() })
	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		tb.Fatal(err)
	}
	return rdb
}

func TestConformance(t *testing.T) {
	ratelimittest.TestStore(t, func(t *testing.T) ratelimit.Store {
		return redisstore.New(newClient(t), redisstore.WithClientTime())
	})
}

// Several app instances, each with its own connection pool and limiter,
// share one limit through Redis. Server time is used, as in production.
func TestInstancesShareOneLimit(t *testing.T) {
	newClient(t) // flush
	const instances, workers, perWorker = 4, 8, 50
	for _, alg := range []ratelimit.Algorithm{
		ratelimit.SlidingLog, ratelimit.SlidingWindow, ratelimit.TokenBucket, ratelimit.LeakyBucket, ratelimit.GCRA,
	} {
		t.Run(alg.String(), func(t *testing.T) {
			// 100 per hour: nothing refills during the test.
			const limit = 100
			var allowed atomic.Int64
			var wg sync.WaitGroup
			for i := 0; i < instances; i++ {
				rdb := redis.NewClient(&redis.Options{Addr: redisAddr(t), PoolSize: workers})
				defer rdb.Close()
				lim, err := ratelimit.New(ratelimit.PerHour(limit), ratelimit.WithAlgorithm(alg),
					ratelimit.WithStore(redisstore.New(rdb, redisstore.WithPrefix(fmt.Sprintf("it:%v:", alg)))))
				if err != nil {
					t.Fatal(err)
				}
				for w := 0; w < workers; w++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for j := 0; j < perWorker; j++ {
							res, err := lim.Allow(context.Background(), "shared")
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
			}
			wg.Wait()
			if got := allowed.Load(); got != limit {
				t.Fatalf("%d instances allowed %d of %d requests, want exactly %d",
					instances, got, instances*workers*perWorker, limit)
			}
		})
	}
}

// Redis Cluster requires all keys of a script in one slot; the hash tag
// guarantees it.
func TestKeysShareASlot(t *testing.T) {
	rdb := newClient(t)
	store := redisstore.New(rdb)
	ctx := context.Background()
	for _, key := range []string{"user:1", "", "a}b", "{x}"} {
		a, err := rdb.ClusterKeySlot(ctx, store.Key(key, "short")).Result()
		if err != nil {
			t.Skipf("CLUSTER KEYSLOT unavailable: %v", err)
		}
		b, _ := rdb.ClusterKeySlot(ctx, store.Key(key, "long_rule")).Result()
		if a != b {
			t.Errorf("key %q: rules in slots %d and %d", key, a, b)
		}
	}
}

func BenchmarkRedis(b *testing.B) {
	rdb := newClient(b)
	for _, alg := range []ratelimit.Algorithm{
		ratelimit.FixedWindow, ratelimit.SlidingLog, ratelimit.SlidingWindow,
		ratelimit.TokenBucket, ratelimit.LeakyBucket, ratelimit.GCRA,
	} {
		lim, err := ratelimit.New(ratelimit.PerSecond(1_000_000), ratelimit.WithAlgorithm(alg),
			ratelimit.WithStore(redisstore.New(rdb)))
		if err != nil {
			b.Fatal(err)
		}
		if alg == ratelimit.SlidingLog {
			// A log of a million entries is the algorithm's worst case;
			// benchmark a realistic size instead.
			lim, _ = ratelimit.New(ratelimit.PerSecond(1000), ratelimit.WithAlgorithm(alg), ratelimit.WithStore(redisstore.New(rdb)))
		}
		ctx := context.Background()
		b.Run(alg.String()+"/serial", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				lim.Allow(ctx, "bench")
			}
		})
		b.Run(alg.String()+"/parallel", func(b *testing.B) {
			b.ReportAllocs()
			var id atomic.Int64
			b.RunParallel(func(pb *testing.PB) {
				key := fmt.Sprint("bench:", id.Add(1))
				for pb.Next() {
					lim.Allow(ctx, key)
				}
			})
		})
	}
}
