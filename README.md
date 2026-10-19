# go-ratelimit

[![CI](https://github.com/tonklasocute/go-ratelimit/actions/workflows/ci.yml/badge.svg)](https://github.com/tonklasocute/go-ratelimit/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/tonklasocute/go-ratelimit.svg)](https://pkg.go.dev/github.com/tonklasocute/go-ratelimit)
[![Go Report Card](https://goreportcard.com/badge/github.com/tonklasocute/go-ratelimit)](https://goreportcard.com/report/github.com/tonklasocute/go-ratelimit)
[![codecov](https://codecov.io/gh/tonklasocute/go-ratelimit/branch/main/graph/badge.svg)](https://codecov.io/gh/tonklasocute/go-ratelimit)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Rate limiting for Go APIs: **six algorithms behind one interface**, an
in-memory store and a **Redis** store that make identical decisions, adapters
for **net/http, chi, Gin, Echo, Fiber and gRPC**, and the IETF **RateLimit**
headers. The core module depends only on the standard library.

- **Algorithms:** fixed window, sliding window log, sliding window counter,
  token bucket, leaky bucket (meter) and GCRA (the default). Each comes with
  [charts and a trade-off guide](docs/algorithms.md).
- **Composite rules:** "10/s **and** 1000/h" is checked atomically. A
  request denied by one rule consumes nothing from the others.
- **Redis:** one Lua script per check (EVALSHA with EVAL fallback), using
  Redis server time so app clocks can drift, with hash-tagged keys for Redis
  Cluster.
- **Memory:** a sharded map, a TTL cleanup goroutine, a key cap with LRU
  eviction, and 0 allocations per check.
- **Middleware:** keys by IP (with an explicit trusted-proxy list), API key,
  user ID, or route + IP; skip rules; JSON 429; custom handlers.
- **Failure policy:** fail open or fail closed when the store is down, plus
  an `OnError` hook.

## Quick start

```sh
go get github.com/tonklasocute/go-ratelimit
```

```go
store := memory.New()
defer store.Close()

lim, err := ratelimit.New(ratelimit.PerSecond(10), ratelimit.WithBurst(20), ratelimit.WithStore(store))
if err != nil {
	log.Fatal(err)
}

http.ListenAndServe(":8080", rlhttp.New(lim)(mux)) // 429 + RateLimit headers, per client IP
```

You can also call the limiter directly:

```go
res, err := lim.Allow(ctx, "user:42")
if err == nil && !res.Allowed {
	// wait res.RetryAfter
}
```

## Modules

The core has no dependencies. Every integration is its own module, so you
download only the frameworks you use.

| Module | Import path | Requires |
|---|---|---|
| Core, memory store, net/http middleware | `github.com/tonklasocute/go-ratelimit` | Go 1.22 |
| Redis store | `github.com/tonklasocute/go-ratelimit/store/redis` | go-redis v9 |
| chi | `github.com/tonklasocute/go-ratelimit/middleware/chi` | chi v5 |
| Gin | `github.com/tonklasocute/go-ratelimit/middleware/gin` | Gin |
| Echo | `github.com/tonklasocute/go-ratelimit/middleware/echo` | Echo v5 |
| Fiber | `github.com/tonklasocute/go-ratelimit/middleware/fiber` | Fiber v3 |
| gRPC | `github.com/tonklasocute/go-ratelimit/middleware/grpc` | grpc-go |

The minimum Go version of each sub-module is the one its framework needs
(1.24–1.26 at the time of writing). The core stays on Go 1.22.

## Algorithms

All six algorithms are fed the same bursty traffic at **10 requests/second,
burst 10**. Each chart has two panels. The top panel shows allowed and denied
requests per 100 ms. The bottom panel shows requests allowed in the trailing
second against the limit.

| | Memory / key | Max in any 1 Period | Burst | Redis cost | Notes |
|---|---|---|---|---|---|
| Fixed window | 2 ints | **2 × Limit** at edges | = Limit | O(1) | simplest; spiky at window edges |
| Sliding window log | Limit timestamps | **exactly Limit** | = Limit | O(log N) | exact, but memory grows with Limit |
| Sliding window counter | 3 ints | ≈ Limit | = Limit | O(1) | cheap and smooth; an approximation |
| Token bucket | float + time | Burst + Limit | tunable | O(1) | the classic API limiter |
| Leaky bucket (meter) | float + time | Burst + Limit | tunable | O(1) | same decisions as token bucket |
| **GCRA** (default) | **1 timestamp** | Burst + Limit | tunable | O(1) | token bucket as one number; no drift |

| Fixed window: 20 in one second at the 3 s edge | Sliding window log: never above 10 |
|---|---|
| ![Fixed window counter](docs/img/fixed_window.svg) | ![Sliding window log](docs/img/sliding_log.svg) |
| **Sliding window counter: smooth, approximate** | **GCRA: burst, then an even 10/s** |
| ![Sliding window counter](docs/img/sliding_window.svg) | ![GCRA](docs/img/gcra.svg) |

The [token bucket](docs/img/token_bucket.svg) and
[leaky bucket](docs/img/leaky_bucket.svg) charts are identical to GCRA's, as
they should be. [docs/algorithms.md](docs/algorithms.md) explains each
algorithm, the trade-offs, and how the Redis scripts stay atomic.

### Which algorithm should I pick?

- **No strong reason to choose?** Use **GCRA**. It keeps the smallest state,
  has an explicit burst, and fits Redis best.
- **"At most N per window, exactly", with a small N:** use the **sliding
  window log**.
- **Large quotas (10 000/day) where approximate is fine:** use the **sliding
  window counter**.
- **Clients expect the counter to reset on the minute:** use the **fixed
  window**.
- **Short bursts and a long quota at once:** combine rules, as shown next.

## Composite rules

```go
lim, err := ratelimit.NewComposite([]ratelimit.Rule{
	{Name: "burst", Rate: ratelimit.PerSecond(10), Burst: 20},                              // GCRA
	{Name: "hourly", Rate: ratelimit.PerHour(1000), Algorithm: ratelimit.SlidingWindow},
}, ratelimit.WithStore(store))
```

A request is allowed only if every rule allows it. The result reports the
most restrictive rule (`res.Policy`). Rules are applied atomically in both
stores, so a request denied by `burst` does not use up `hourly`.

## Stores

### Memory

```go
store := memory.New(
	memory.WithShards(64),                 // default 64
	memory.WithMaxKeys(1_000_000),         // LRU eviction per shard; default unlimited
	memory.WithCleanupInterval(time.Minute),
)
defer store.Close() // stops the cleanup goroutine
```

The memory store keeps state per process. With several instances, each
enforces its own limit.

### Redis

```go
rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"}) // or ClusterClient, UniversalClient
lim, err := ratelimit.New(ratelimit.PerSecond(100),
	ratelimit.WithStore(redisstore.New(rdb, redisstore.WithPrefix("myapp:rl:"))),
	ratelimit.WithFailurePolicy(ratelimit.FailOpen), // or FailClosed → 503
	ratelimit.WithOnError(func(ctx context.Context, key string, err error) { log.Print(err) }),
)
```

- **One round trip per check.** One Lua script applies all rules
  atomically. It runs by SHA with `EVALSHA` and falls back to `EVAL` after a
  Redis restart or `SCRIPT FLUSH`.
- **Redis `TIME` by default.** Instances with skewed clocks still share one
  correct limit. `WithClientTime()` exists for deterministic tests.
- **Cluster-safe.** Keys are `myapp:rl:{<key>}:<rule>`, and the hash tag
  keeps all rules of a key in one slot.
- **Self-cleaning.** Keys expire when the state is fresh again.

#### Proof: two instances, one limit

The distributed example ([examples/distributed](examples/distributed)) runs
two app instances that share one Redis. Each instance is configured with
**20 req/s per API key, burst 20**. k6 sends **60 req/s for 20 s** with one
API key and alternates between the instances. If the limit were per
instance, about 40 req/s would get through. With the shared limit, 20 req/s
should get through, split between the two instances.

```text
Shared rate limit across 2 instances
  configured        20 req/s per API key, burst 20
  offered           1201 requests in 20s (60 req/s)
  allowed           420  (app1 209, app2 211)
  rejected (429)    781
  expected allowed  ≈ 420 (burst + limit × duration)
  sustained rate    20.00 req/s after the burst
  thresholds        passed
```

Per second, both instances together allow exactly 20, about 10 each:

```text
second   app1   app2  allowed  rejected
0          19     17       36        14   ← burst of 20 + 16 refilled
1          10     10       20        40
2          10     10       20        40
⋮
13         10     10       20        40
14          9     11       20        40
15         10     10       20        40
16          9     11       20        40
17         10     10       20        40
18         10     10       20        40
19         11      9       20        40
20          1      3        4         7   ← partial second
total                     420       781
```

Every whole second from 1 to 19 allowed 20 requests in total. The allowed
total matches `burst + limit × duration` = 20 + 20 × 20 = 420 exactly. The
seconds are wall-clock seconds, so the first and last are partial.

A second run gave the same total of 420 (app1 208, app2 212). That time the
burst spread over seconds 0 and 1 (21 and 25), because the test started late
in a wall-clock second, and every later whole second again allowed exactly
20. Both runs used Docker Desktop 29.8.0 on an Apple M1, Redis 7.4.11 and
k6 1.3.0.
Reproduce it with:

```sh
make compose                         # builds, runs k6, prints the summary
examples/distributed/per-second.sh   # per-second table from results/k6.csv
```

## Middleware

Every adapter has the same options: key function, header style, JSON body,
skip, deny handler and error handler. Denials get `429` and `Retry-After`.
A missing key gets `400`. A limiter that fails closed gets `503`.

```go
// net/http (core module)
h := rlhttp.New(lim,
	rlhttp.WithKeyFunc(rlhttp.FirstOf(rlhttp.KeyByHeader("X-API-Key"), rlhttp.KeyByIP(nil))),
	rlhttp.WithJSONBody(),
	rlhttp.WithSkip(func(r *http.Request) bool { return r.URL.Path == "/healthz" }),
)(mux)

// chi: per route pattern, so /users/1 and /users/2 share "GET /users/{id}"
r.Use(rlchi.New(lim, rlhttp.WithKeyFunc(rlchi.KeyByRouteAndIP(nil))))

// Gin: per authenticated user
r.Use(rlgin.New(lim, rlgin.WithKeyFunc(rlgin.KeyByContextValue("user_id"))))

// Echo v5
e.Use(rlecho.New(lim, rlecho.WithKeyFunc(rlecho.KeyByRouteAndIP(nil))))

// Fiber v3
app.Use(rlfiber.New(lim, rlfiber.WithKeyFunc(rlfiber.KeyByLocal("user_id"))))

// gRPC: RESOURCE_EXHAUSTED with a RetryInfo detail
grpc.NewServer(
	grpc.ChainUnaryInterceptor(rlgrpc.UnaryServerInterceptor(lim)),
	grpc.ChainStreamInterceptor(rlgrpc.StreamServerInterceptor(lim)),
)
```

### Client IP and proxies

`KeyByIP(nil)` uses the TCP peer address and **ignores `X-Forwarded-For`**.
That header is set by the client, so trusting it blindly lets anyone get a
fresh limit on every request by sending a made-up address, or exhaust
someone else's limit by sending theirs. Behind your own load balancer, list
it explicitly:

```go
ips, err := middleware.NewIPResolver(
	middleware.TrustProxies("10.0.0.0/8"),   // only proxies you run
	middleware.IPv6Prefix(64),               // one limit per IPv6 /64, not per address
)
key := rlhttp.KeyByIP(ips)
```

With trusted proxies, `X-Forwarded-For` is read from right to left, and the
first address that is not a trusted proxy is the client. For CDNs that send a
single-address header, use `middleware.TrustHeader("CF-Connecting-IP")`.

## Response headers

The default follows the IETF draft
[draft-ietf-httpapi-ratelimit-headers-11](https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/):

```http
HTTP/1.1 429 Too Many Requests
RateLimit-Policy: "burst";q=20;w=2, "hourly";q=1000;w=3600
RateLimit: "burst";r=0;t=2
Retry-After: 1
Content-Type: application/json

{"error":"rate limit exceeded","limit":20,"remaining":0,"retry_after":1}
```

Older styles can be turned on alongside it:

```go
rlhttp.WithHeaders(middleware.HeadersIETF | middleware.HeadersIETFLegacy | middleware.HeadersXRateLimit)
// RateLimit-Limit / -Remaining / -Reset (earlier drafts, delta seconds)
// X-RateLimit-Limit / -Remaining / -Reset (Unix time, as GitHub does)
```

## Benchmarks

The benchmarks run on an Apple M1 (8 cores, 16 GB) with macOS 27.0.1 and
go1.27.1, using `make bench`. The memory store numbers include the limiter
and `time.Now()`. The rule is 2³⁰/s, so requests are allowed and every check
runs the full path; sliding log uses 1000/s.

| Algorithm | Algorithm only | Memory, serial | Memory, parallel (key per goroutine) | Memory, one hot key (8 goroutines) | Allocs |
|---|---|---|---|---|---|
| Fixed window | 14.6 ns | 85.8 ns | 17.6 ns | 204 ns | 0 |
| Sliding log | 12.6 ns | 85.1 ns | 17.7 ns | 177 ns | 0 |
| Sliding window | 15.2 ns | 96.9 ns | 19.7 ns | 213 ns | 0 |
| Token bucket | 17.5 ns | 92.0 ns | 18.8 ns | 210 ns | 0 |
| Leaky bucket | 17.5 ns | 91.9 ns | 18.8 ns | 211 ns | 0 |
| GCRA | 11.9 ns | 88.3 ns | 17.9 ns | 207 ns | 0 |
| Two rules (GCRA + sliding window), parallel | | | 26.5 ns | | 0 |

**Redis store.** The client ran in a Linux container (go1.26.9) on the same
Docker network as `redis:7-alpine` (7.4.11). Both ran under Docker Desktop
29.8.0 on the M1 (8 vCPUs). "Server" is the time Redis spent executing the
script per check, read from `INFO commandstats`; the rest is the round trip.
Parallel runs use 8 goroutines, each with its own key, over the default
go-redis connection pool.

| Algorithm | Serial | Server | Parallel (8 goroutines) | Server | Allocs |
|---|---|---|---|---|---|
| Fixed window | 86.3 µs | 19.5 µs | 21.3 µs | 11.8 µs | 18–19 |
| Sliding log | 81.4 µs | 18.9 µs | 23.3 µs | 13.9 µs | 18 |
| Sliding window | 78.6 µs | 18.1 µs | 22.3 µs | 12.7 µs | 19 |
| Token bucket | 81.5 µs | 18.7 µs | 21.2 µs | 12.0 µs | 18 |
| Leaky bucket | 73.0 µs | 16.7 µs | 21.2 µs | 11.9 µs | 18 |
| GCRA | 73.2 µs | 16.1 µs | 20.7 µs | 11.2 µs | 18 |

- **The network dominates.** The script runs in 11–20 µs, and the algorithm
  barely matters. Most of the serial time is the round trip through Docker's
  virtual network.
- **Throughput:** one process sustained about 47 000 checks/s with 8
  goroutines.
- **Allocations** come from go-redis building and parsing the command.
- **From macOS:** run on macOS against the container through Docker
  Desktop's port forwarding, the serial time rises to 154–182 µs. The server
  time is unchanged.

Measure against your own Redis with `REDIS_ADDR=host:6379 make bench-redis`,
or let the benchmark start a container with Docker.

## Comparison with other libraries

These are all good libraries. Pick the one that fits.

| | go-ratelimit | [ulule/limiter](https://github.com/ulule/limiter) | [go-redis/redis_rate](https://github.com/go-redis/redis_rate) | [throttled](https://github.com/throttled/throttled) | [uber-go/ratelimit](https://github.com/uber-go/ratelimit) | [x/time/rate](https://pkg.go.dev/golang.org/x/time/rate) |
|---|---|---|---|---|---|---|
| Purpose | per-key API limits | per-key API limits | per-key limits on Redis | per-key API limits | pace your own outgoing work | in-process limiter |
| Algorithms | 6 | fixed window | GCRA | GCRA | leaky bucket (blocks) | token bucket |
| In-memory store | ✓ | ✓ | – | ✓ | in-process | in-process |
| Redis | ✓ (Lua, server time) | ✓ | ✓ (Lua) | ✓ | – | – |
| Multiple rules, atomic | ✓ | – | – | – | – | – |
| Middleware | net/http, chi, Gin, Echo, Fiber, gRPC | net/http, Gin, fasthttp | – | net/http | – | – |
| Response headers | IETF draft-11, legacy, X-RateLimit | X-RateLimit | – | X-RateLimit, Retry-After | – | – |
| Client IP behind proxies | explicit trusted-proxy list | opt-in `TrustForwardHeader` | – | user-defined (`VaryBy`) | – | – |

- **ulule/limiter** is mature and widely used. Choose it if one fixed window
  is enough and you are on its supported frameworks. It ships as a single
  module, so its `go.mod` brings in Gin, fasthttp and go-redis.
- **redis_rate** is a small, focused GCRA-on-Redis library. Choose it if you
  only need that and no middleware.
- **uber-go/ratelimit** and **x/time/rate** solve a different problem:
  pacing work inside one process (`Take()`/`Wait()` block). They have no
  per-key storage and no shared state.

## FAQ

**Which algorithm should I pick?**
GCRA, unless you need something
[listed above](#which-algorithm-should-i-pick).

**Why did 19 requests pass in one second with a 10/s limit?**
Bucket algorithms allow `Burst + Limit` in any one second: a full bucket of
10, plus 10 refilled during that second. If "10/s" must mean "never more
than 10 in any second", use the sliding window log, or a small burst.

**Should I fail open or closed?**
Fail open (the default) when the limiter protects capacity: a Redis outage
should not take your API down. Fail closed when it protects something
sensitive, such as login attempts or SMS sends. Log every error with
`WithOnError` either way.

**How much memory does the memory store use?**
About 180 bytes per key with one rule (measured with 200 000 keys), plus the
key string. Sliding log adds 8 bytes per logged request. Cap it with
`memory.WithMaxKeys`.

**Can two limiters share one store?**
Yes. The rule name is part of the storage key. Give each limiter distinct
names unless you want them to share counters.

**How do I test code that uses a limiter?**
Inject `ratelimittest.NewClock()` with `ratelimit.WithClock` and move it with
`Advance`. If you write your own store, run `ratelimittest.TestStore` against
it.

## Development

```sh
make race        # tests with -race in every module
make cover       # core coverage (currently 97%)
make lint        # golangci-lint in every module
make integration # Redis tests (REDIS_ADDR or Docker)
make visualize   # regenerate docs/img
```

See [CONTRIBUTING.md](CONTRIBUTING.md). Releases tag every module together;
see [CHANGELOG.md](CHANGELOG.md#releasing).

## License

[MIT](LICENSE) © Khomkrit Daengnuan
