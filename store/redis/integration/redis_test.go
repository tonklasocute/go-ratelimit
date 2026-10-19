package integration

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
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
// guarantees it. The slot comes from CLUSTER KEYSLOT on a cluster-enabled
// server, otherwise from the cluster spec's algorithm below.
func TestKeysShareASlot(t *testing.T) {
	if got := crc16([]byte("123456789")); got != 0x31C3 { // test vector from the cluster spec
		t.Fatalf("crc16 = %#x", got)
	}
	rdb := newClient(t)
	store := redisstore.New(rdb)
	ctx := context.Background()
	slot := func(key string) int64 {
		if s, err := rdb.ClusterKeySlot(ctx, key).Result(); err == nil {
			return s
		}
		return keySlot(key)
	}
	for _, key := range []string{"user:1", "", "a}b", "{x}", "192.0.2.1"} {
		a, b := slot(store.Key(key, "short")), slot(store.Key(key, "long_rule"))
		if a != b {
			t.Errorf("key %q: rules in slots %d and %d", key, a, b)
		}
	}
	// Different keys still spread over slots.
	if slot(store.Key("user:1", "r")) == slot(store.Key("user:2", "r")) {
		t.Error("distinct keys share a slot; the hash tag is not keyed on the user key")
	}
}

// keySlot is HASH_SLOT from the Redis Cluster specification: CRC16 of the
// first non-empty {hash tag}, or of the whole key, modulo 16384.
func keySlot(key string) int64 {
	if i := strings.IndexByte(key, '{'); i >= 0 {
		if j := strings.IndexByte(key[i+1:], '}'); j > 0 {
			key = key[i+1 : i+1+j]
		}
	}
	return int64(crc16([]byte(key)) % 16384)
}

// crc16 is CRC-16/XMODEM, the variant Redis Cluster uses.
func crc16(b []byte) uint16 {
	var crc uint16
	for _, c := range b {
		crc ^= uint16(c) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// BenchmarkRedis reports the client-observed time per check (dominated by
// the network round trip) and server-µs/op, the time Redis spent running the
// script, read from INFO commandstats.
func BenchmarkRedis(b *testing.B) {
	rdb := newClient(b)
	for _, alg := range []ratelimit.Algorithm{
		ratelimit.FixedWindow, ratelimit.SlidingLog, ratelimit.SlidingWindow,
		ratelimit.TokenBucket, ratelimit.LeakyBucket, ratelimit.GCRA,
	} {
		rate := ratelimit.PerSecond(1_000_000) // mostly allowed: the full path runs
		if alg == ratelimit.SlidingLog {
			rate = ratelimit.PerSecond(1000) // the log grows with the limit
		}
		lim, err := ratelimit.New(rate, ratelimit.WithAlgorithm(alg), ratelimit.WithStore(redisstore.New(rdb)))
		if err != nil {
			b.Fatal(err)
		}
		ctx := context.Background()
		b.Run(alg.String()+"/serial", func(b *testing.B) {
			b.ReportAllocs()
			resetStats(b, rdb)
			for i := 0; i < b.N; i++ {
				lim.Allow(ctx, "bench")
			}
			reportServerTime(b, rdb)
		})
		b.Run(alg.String()+"/parallel", func(b *testing.B) {
			b.ReportAllocs()
			resetStats(b, rdb)
			var id atomic.Int64
			b.RunParallel(func(pb *testing.PB) {
				key := fmt.Sprint("bench:", id.Add(1))
				for pb.Next() {
					lim.Allow(ctx, key)
				}
			})
			reportServerTime(b, rdb)
		})
	}
}

func resetStats(b *testing.B, rdb *redis.Client) {
	b.Helper()
	if err := rdb.ConfigResetStat(context.Background()).Err(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
}

// reportServerTime reports the average execution time of EVALSHA and EVAL
// since the last CONFIG RESETSTAT.
func reportServerTime(b *testing.B, rdb *redis.Client) {
	b.Helper()
	b.StopTimer()
	info, err := rdb.Info(context.Background(), "commandstats").Result()
	if err != nil {
		b.Fatal(err)
	}
	var calls, usec float64
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "cmdstat_evalsha:") && !strings.HasPrefix(line, "cmdstat_eval:") {
			continue
		}
		for _, field := range strings.Split(line[strings.IndexByte(line, ':')+1:], ",") {
			k, v, _ := strings.Cut(field, "=")
			n, _ := strconv.ParseFloat(v, 64)
			switch k {
			case "calls":
				calls += n
			case "usec":
				usec += n
			}
		}
	}
	if calls > 0 {
		b.ReportMetric(usec/calls, "server-µs/op")
	}
}
