# Rate limiting algorithms

This guide explains the six algorithms in go-ratelimit, what each one costs,
and how they behave in a distributed setup. It follows the structure of the
rate limiter chapter of *System Design Interview* (Alex Xu, vol. 1, ch. 4):
where a limiter sits, which algorithm to use, how to store counters, and how
to keep a shared limit correct when many servers update it at once.

- [Where the limiter sits](#where-the-limiter-sits)
- [The algorithms](#the-algorithms)
- [Trade-offs at a glance](#trade-offs-at-a-glance)
- [Which one should I pick?](#which-one-should-i-pick)
- [Bursts: why bucket algorithms allow up to 2× in one second](#bursts-why-bucket-algorithms-allow-up-to-2-in-one-second)
- [Distributed limits: race conditions and atomicity](#distributed-limits-race-conditions-and-atomicity)
- [Telling clients about limits](#telling-clients-about-limits)
- [When the store fails](#when-the-store-fails)
- [References](#references)

## Where the limiter sits

The chapter compares putting the limiter in the client (easy to bypass), in
the server, or in middleware/an API gateway in front of the server. This
library is server-side middleware: it runs inside your service (net/http,
chi, Gin, Echo, Fiber, gRPC) before your handlers. That lets you limit on
things a gateway does not know, such as an authenticated user ID or a plan,
and still share counters across instances through Redis.

A limiter has three parts, each replaceable:

```
request ──► key extractor ──► Limiter (rules × algorithm) ──► Store (memory | Redis)
             IP / API key /      "10/s AND 1000/h"              state per key and rule
             user / route
```

## The algorithms

All charts replay the same traffic at a limit of **10 requests per second
with burst 10**:

- steady traffic at 6/s;
- a burst of 30 requests straddling the 3 s window edge;
- two seconds of overload at 25/s;
- an idle gap, then a spike of 30 requests at once;
- mild overload at 14/s.

The top panel shows allowed and denied requests per 100 ms. The bottom panel
shows how many requests were allowed in the trailing second, against the
limit. Regenerate them with `go run ./cmd/visualize`.

| Algorithm | Allowed / offered | Max allowed in any 1 s (limit 10) |
|---|---|---|
| Fixed window counter | 73 / 142 | 20 |
| Sliding window log | 60 / 142 | 10 |
| Sliding window counter | 64 / 142 | 16 |
| Token bucket | 74 / 142 | 19 |
| Leaky bucket (meter) | 74 / 142 | 19 |
| GCRA | 74 / 142 | 19 |

### 1. Fixed window counter

Cut time into windows of length `Period` aligned to the clock, keep one
counter per window, and allow while `count + n ≤ Limit`.

![Fixed window counter](img/fixed_window.svg)

- **Memory per key:** two integers (window start, count).
- **Accuracy at window edges:** poor. Look at the 3 s edge: 10 requests at
  the end of one window and 10 more at the start of the next all pass, so
  20 requests get through within a few hundred milliseconds. In general up to
  2 × Limit can pass within one Period.
- **Bursts:** up to Limit at any instant. Every client's window resets at
  the same moment, which can synchronize retries into spikes.
- **Distributed:** the simplest of all. In Redis, an `INCR` and `EXPIRE` on
  a key named after the window is enough.

### 2. Sliding window log

Store a timestamp for every allowed request. Drop timestamps older than
`Period`, and allow if fewer than `Limit` remain.

![Sliding window log](img/sliding_log.svg)

- **Memory per key:** one timestamp per allowed request, up to Limit. That is
  80 KB per key for 10 000/hour.
- **Accuracy:** exact. No interval of length Period ever contains more than
  Limit allowed requests. The chart's trailing-second line never crosses 10.
- **Bursts:** up to Limit at once. After that, a request is admitted only
  when an old entry expires, exactly one Period after it was logged. The spike
  of 30 at 6.5 s locks the key until 7.5 s, even though traffic keeps arriving
  at 14/s, and then admissions resume at the pace at which those entries expire.
- **Distributed:** works (a Redis sorted set: `ZREMRANGEBYSCORE`, `ZCARD`,
  `ZADD`), but memory and CPU grow with Limit.

This implementation logs only *allowed* requests. The variant in the book
also logs rejected ones, which keeps a client that keeps hammering locked out
for longer.

### 3. Sliding window counter (weighted)

A hybrid of the two above. Keep the counters of the current and the previous
fixed window, and estimate the count of the sliding window by weighting the
previous window by how much of it the sliding window still covers:

```
estimate = previous × (1 − elapsed/Period) + current
```

![Sliding window counter](img/sliding_window.svg)

- **Memory per key:** three integers.
- **Accuracy:** approximate. It assumes the previous window's requests were
  spread evenly. In the chart, the trailing count peaks at 16 around 3.8 s.
  The 8 requests allowed at the end of the [2 s, 3 s) window were bunched,
  not spread, so as the overload starts the estimate undercounts them.
  Cloudflare measured only 0.003% of requests wrongly allowed or limited
  across 400 million requests of real traffic.
- **Bursts:** up to Limit at once. After a full window, capacity returns
  gradually as the previous count decays.
- **Distributed:** excellent. It costs about as little as fixed window and is
  much smoother.

### 4. Token bucket

A bucket holds up to `Burst` tokens and refills at `Limit / Period` tokens per
second. Each request takes `n` tokens. The refill is computed lazily from the
time elapsed, so no timer runs per key.

![Token bucket](img/token_bucket.svg)

- **Memory per key:** a token count (float) and a timestamp.
- **Accuracy:** there are no windows. Over any interval of length *d*, at
  most `Burst + d × rate` requests pass.
- **Bursts:** explicit and independent of the sustained rate. You can allow
  bursts of 50 at a sustained 10/s. This is the model used by AWS API Gateway
  and Stripe.
- **Distributed:** good. The refill depends on the current time, so all
  instances must agree on the clock. The Redis store reads Redis' own `TIME`.

### 5. Leaky bucket (as a meter)

A bucket of capacity `Burst` leaks at `Limit / Period`. Each request pours `n`
units in, and a request that would overflow is denied.

![Leaky bucket](img/leaky_bucket.svg)

This "meter" is the token bucket viewed upside down (`level = Burst −
tokens`), and it makes **exactly** the same decisions. A test checks this on
20 000 random requests. The book's leaky bucket is a different thing: a
**queue** drained at a constant rate, which delays requests instead of
rejecting them. That is traffic shaping, as done by NGINX `limit_req` with
delay or uber-go/ratelimit, and it is out of scope for a library whose job is
to answer "allowed or not" right now.

- **Memory, accuracy, bursts, distributed:** as for the token bucket.

### 6. GCRA (generic cell rate algorithm)

GCRA comes from ATM networks. It stores a single timestamp, the *theoretical
arrival time* (TAT), which is when the next request would be exactly on
schedule. With emission interval `T = Period / Limit`:

```
newTAT  = max(TAT, now) + n × T
allowAt = newTAT − Burst × T
allowed if now ≥ allowAt, then TAT = newTAT
```

![GCRA](img/gcra.svg)

- **Memory per key:** one 8-byte timestamp, the smallest of all six.
- **Accuracy and bursts:** identical to the token bucket (tested on 20 000
  random requests), with no floating point and therefore no drift.
- **Distributed:** the best fit. One read and one write in Redis, the key
  expires exactly at TAT, and there is nothing to refill.

This is the default algorithm. go-redis/redis_rate and throttled use it too.

## Trade-offs at a glance

| | Memory per key | Over any 1 Period | Burst control | Cost in Redis | Smoothness |
|---|---|---|---|---|---|
| Fixed window | 2 ints | up to 2 × Limit | = Limit | 1 hash, O(1) | spiky at edges |
| Sliding log | Limit timestamps | exactly ≤ Limit | = Limit | sorted set, O(log N) | clumps |
| Sliding window counter | 3 ints | approx. Limit | = Limit | 1 hash, O(1) | good |
| Token bucket | float + timestamp | ≤ Burst + Limit | independent (Burst) | 1 hash, O(1) | smooth |
| Leaky bucket (meter) | float + timestamp | ≤ Burst + Limit | independent (Burst) | 1 hash, O(1) | smooth |
| GCRA | 1 timestamp | ≤ Burst + Limit | independent (Burst) | 1 hash, O(1) | smooth |

## Which one should I pick?

- **No strong reason to choose?** Use **GCRA** (the default): smallest
  state, smooth, explicit burst, ideal for Redis.
- **"At most N per hour, exactly", for example billing or an abuse threshold
  with a small N:** use the **sliding window log**. Avoid it for large N.
- **Large quotas such as 10 000/day where approximate is fine:** use the
  **sliding window counter**. It is cheap and has no 2× edge.
- **Matching an existing "N per calendar minute" contract, where clients
  expect the counter to reset on the minute:** use the **fixed window**.
- **You think in "bucket size and refill rate":** use the **token bucket**,
  or GCRA, which makes the same decisions.
- **Combine them.** For example, GCRA at 10/s for bursts and a sliding window
  counter at 1000/h for the quota, enforced atomically (see below).

## Bursts: why bucket algorithms allow up to 2× in one second

In the charts, the bucket algorithms allow up to 19 requests in one second at
a "10/s" limit. That is by design, not a bug. A full bucket holds `Burst`
tokens and earns `Limit` more every second, so the most that can pass in any
one second is `Burst + Limit`. If 10 per second must mean "never more than 10
in any second", use the sliding window log, or set a small `Burst`. The
property tests check each algorithm's own theoretical maximum over two
simulated hours of bursty traffic.

## Distributed limits: race conditions and atomicity

With several app instances, the counters must live in a shared store, and the
book points out the classic race. Two instances read `count = 9` at the same
time, both decide "allowed", and both write `10`. Eleven requests passed a
limit of 10. Locks would fix it but cost a round trip and are fragile.

The Redis store avoids the race with **one Lua script per check**
([`store/redis/ratelimit.lua`](../store/redis/ratelimit.lua)):

1. **Atomic read-decide-write.** Redis runs a script to completion without
   interleaving commands from other clients, so no other instance can change
   a counter between the read and the write. The integration test runs four
   instances with eight goroutines each against one key and checks that
   *exactly* the limit is allowed.
2. **All-or-nothing across rules.** A check with several rules ("10/s AND
   1000/h") first evaluates every rule, then writes only if all of them
   allow the request. A request denied by the per-second rule does not use up
   the hourly quota. The memory store does the same under one shard lock.
3. **Server time.** The script reads the clock with `redis.call("TIME")`, so
   every instance uses the same time even when the app servers' clocks
   disagree. `redis.replicate_commands()` is called first; Redis versions
   before 5 need it before running a non-deterministic command, and later
   versions treat it as a no-op. Effect replication sends the resulting
   writes to replicas instead of re-running `TIME` there.
4. **Redis Cluster.** A multi-key script must touch keys in a single slot.
   Keys are named `prefix:{key}:rule`, and the `{hash tag}` makes Redis hash
   only `key`, so every rule of a key lands in the same slot. Characters that
   would end the tag early are escaped.
5. **Script caching.** The store calls `EVALSHA` with the script's SHA1, and
   falls back to `EVAL` if Redis answers `NOSCRIPT` (after a restart, a
   failover or `SCRIPT FLUSH`). `EVAL` also caches the script for next time.
6. **Expiry.** Each key gets a `PEXPIRE` equal to the time until its state is
   fresh again, so idle keys disappear on their own and memory follows the
   number of active clients.
7. **Same decisions as Go.** The script mirrors the Go formulas in
   microseconds. Redis' Lua uses doubles, and microsecond Unix times
   (~1.8 × 10¹⁵) are exact. A differential test feeds the same random
   traffic to the memory store and the script and requires identical results.

In memory, the store hashes keys to shards, each with its own mutex. A check
takes one shard lock, so unrelated keys rarely contend, and all rules of a
key are updated together.

## Telling clients about limits

The middleware sends the headers of the IETF draft
[draft-ietf-httpapi-ratelimit-headers-11](https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/)
by default:

```
RateLimit-Policy: "gcra_10_1s";q=10;w=1, "sliding_window_1000_1h";q=1000;w=3600
RateLimit: "gcra_10_1s";r=0;t=1
Retry-After: 1
```

`RateLimit-Policy` lists every rule. `RateLimit` reports the most restrictive
rule for this request: `r` is the remaining quota and `t` is the seconds until
it is fully available again. For bucket algorithms, `q` is the burst and `w`
is the time to refill it. Earlier drafts used `RateLimit-Limit`,
`RateLimit-Remaining` and `RateLimit-Reset`, and many APIs use
`X-RateLimit-*`. Both styles can be turned on as well. A denied request gets
`429 Too Many Requests` with `Retry-After`. gRPC gets `RESOURCE_EXHAUSTED`
with a `RetryInfo` detail.

## When the store fails

When Redis is unreachable, the limiter either **fails open** (allow, the
default, which favors availability) or **fails closed** (deny with 503, which
favors protection, for example for login endpoints). In both cases an
`OnError` hook receives the error for logging and alerting.

## References

- Alex Xu, *System Design Interview – An Insider's Guide*, vol. 1, chapter 4
  "Design a Rate Limiter".
- Cloudflare, [How we built rate limiting capable of scaling to millions of domains](https://blog.cloudflare.com/counting-things-a-lot-of-different-things/)
  (sliding window counter).
- Stripe, [Scaling your API with rate limiters](https://stripe.com/blog/rate-limiters).
- Brandur Leach, [Rate Limiting, Cells, and GCRA](https://brandur.org/rate-limiting).
- IETF HTTPAPI WG, [RateLimit header fields for HTTP](https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/)
  (draft 11); [RFC 9651](https://www.rfc-editor.org/rfc/rfc9651) Structured Field Values.
- Redis, [Scripting with Lua](https://redis.io/docs/latest/develop/interact/programmability/eval-intro/)
  and [Cluster hash tags](https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/#hash-tags).
