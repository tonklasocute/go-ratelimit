package algorithm

// Token bucket
//
// A bucket holds up to Burst tokens and is refilled at Limit tokens per
// Period. Each request takes n tokens; if there are not enough, it is denied.
// Refilling is computed lazily from the time since the last request, so no
// background timer is needed.
//
//	Limit = 10/s (1 token / 100ms), Burst = 5
//
//	          refill +1 per 100ms
//	               │
//	          ┌────▼────┐  cap 5
//	          │ ● ● ● ● │  tokens = 4
//	          │ ●       │
//	          └────┬────┘
//	               │ take n per request, deny if tokens < n
//	               ▼
//
//	t=0     5 requests → tokens 0      (burst)
//	t=0     1 request  → denied, retry after 100ms
//	t=250ms tokens 2.5 → 2 requests fit
//
// Memory per key: a float and a timestamp.
//
// Accuracy at window edges: there are no windows. Over any interval d at most
// Burst + d×rate requests are allowed.
//
// Bursts: explicit and tunable (Burst), independent of the sustained rate.
// The usual choice for public APIs (AWS API Gateway, Stripe).
//
// Distributed-friendliness: good. Two fields per key, but the refill needs
// the current time, so all instances must agree on the clock (the Redis store
// uses the server's TIME).

func tokenBucketPeek(st *State, p Params, now, n int64) Decision {
	capacity := float64(p.Burst)
	if st.A == 0 {
		st.A, st.F = now, capacity
	} else if now > st.A {
		refill := float64(now-st.A) * float64(p.Limit) / float64(p.Period)
		st.A, st.F = now, min(capacity, st.F+refill)
	}
	// If the clock went backwards, nothing refills until it passes st.A.
	wait := float64(max(st.A-now, 0))
	nsPerToken := float64(p.Period) / float64(p.Limit)

	d := Decision{Limit: p.Burst}
	tokens := st.F
	if tokens+eps >= float64(n) {
		d.Allowed = true
		tokens -= float64(n)
	} else if n <= p.Burst {
		d.RetryAfter = ceilDur(wait + (float64(n)-tokens)*nsPerToken)
	}
	d.Remaining = floorInt(tokens)
	d.ResetAfter = ceilDur(wait + (capacity-tokens)*nsPerToken)
	return d
}

func tokenBucketCommit(st *State, n int64) { st.F = max(st.F-float64(n), 0) }
