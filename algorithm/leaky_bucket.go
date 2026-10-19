package algorithm

// Leaky bucket (as a meter)
//
// A bucket of capacity Burst leaks at Limit units per Period. Each request
// pours n units in; if that would overflow, the request is denied and nothing
// is poured. Like the token bucket, the leak is computed lazily.
//
//	Limit = 10/s (leaks 1 unit / 100ms), Burst = 5
//
//	   request: pour n ──►┌─────────┐  cap 5 — deny if level + n > 5
//	                      │         │
//	                      │~~~~~~~~~│  level = 3
//	                      │█████████│
//	                      └────┬────┘
//	                           ▼  leaks 1 unit / 100ms
//
// This "meter" form is the mirror image of the token bucket (level =
// Burst - tokens) and makes exactly the same decisions; GCRA is the same
// meter again, stored as a single timestamp. It is a different algorithm from
// the "leaky bucket as a queue" (used for traffic shaping, e.g. NGINX
// limit_req or uber-go/ratelimit), which delays requests to emit them at a
// constant rate instead of rejecting them.
//
// Memory per key: a float and a timestamp.
//
// Accuracy at window edges: no windows; at most Burst + d×rate requests in
// any interval d.
//
// Bursts: up to Burst at once, then exactly the sustained rate.
//
// Distributed-friendliness: good, with the same clock caveat as the token
// bucket.

func leakyBucketPeek(st *State, p Params, now, n int64) Decision {
	if now > st.A {
		leaked := float64(now-st.A) * float64(p.Limit) / float64(p.Period)
		st.A, st.F = now, max(st.F-leaked, 0)
	}
	// If the clock went backwards, nothing leaks until it passes st.A.
	wait := float64(max(st.A-now, 0))
	nsPerUnit := float64(p.Period) / float64(p.Limit)
	capacity := float64(p.Burst)

	d := Decision{Limit: p.Burst}
	level := st.F
	if level+float64(n) <= capacity+eps {
		d.Allowed = true
		level += float64(n)
	} else if n <= p.Burst {
		d.RetryAfter = ceilDur(wait + (level+float64(n)-capacity)*nsPerUnit)
	}
	d.Remaining = floorInt(capacity - level)
	d.ResetAfter = ceilDur(wait + level*nsPerUnit)
	return d
}

func leakyBucketCommit(st *State, n int64) { st.F += float64(n) }
