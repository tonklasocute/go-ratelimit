package algorithm

import "time"

// Sliding window counter (weighted)
//
// Keep fixed-window counters for the current and the previous window, and
// estimate the number of requests in the last Period by assuming the previous
// window's requests were spread evenly:
//
//	estimate = previous × (overlap of the sliding window with it) + current
//
//	Limit = 10, Period = 1s, now = 1.3s
//
//	            previous [0s, 1s)       current [1s, 2s)
//	            count = 10              count = 2
//	         ├───────────────────────┼───────────────────────┤
//	                 ├────── sliding window (0.3s, 1.3s] ─────┤
//	                 └── 70% of previous ─┘
//
//	estimate = 10 × 0.7 + 2 = 9  →  one more request fits
//
// Memory per key: three integers (window start, current and previous count).
//
// Accuracy at window edges: approximate. It removes most of the fixed
// window's double burst; when the previous window's traffic was bunched up
// rather than even, the estimate is off. Cloudflare measured 0.003% of
// requests wrongly allowed or limited on real traffic.
//
// Bursts: up to Limit at once; after a full window the next one opens
// gradually as the previous count decays.
//
// Distributed-friendliness: excellent. Two counters per key, cheap to update
// atomically in Redis.

func slidingWindowPeek(st *State, p Params, now, n int64) Decision {
	period := int64(p.Period)
	if start := now - now%period; start > st.A {
		if start-period == st.A {
			st.C = st.B
		} else {
			st.C = 0
		}
		st.A, st.B = start, 0
	}
	elapsed := max(now-st.A, 0)
	est := float64(st.C)*float64(period-elapsed)/float64(period) + float64(st.B)
	limit := float64(p.Limit)

	d := Decision{Limit: p.Limit}
	current := st.B
	if est+float64(n) <= limit+eps {
		d.Allowed = true
		d.Remaining = floorInt(limit - est - float64(n))
		current += n
	} else {
		d.Remaining = floorInt(limit - est)
		if n <= p.Limit {
			d.RetryAfter = slidingWindowRetry(st, p, now, n)
		}
	}
	switch {
	case current > 0: // counts until the end of the next window
		d.ResetAfter = time.Duration(st.A + 2*period - now)
	case st.C > 0:
		d.ResetAfter = time.Duration(st.A + period - now)
	}
	return d
}

// slidingWindowRetry is the time until n requests fit.
func slidingWindowRetry(st *State, p Params, now, n int64) time.Duration {
	period := float64(p.Period)
	if st.B+n <= p.Limit {
		// Fits in this window once the previous count has decayed enough:
		// C × (P - e) / P + B + n ≤ L.
		e := period - float64(p.Limit-st.B-n)*period/float64(st.C)
		return ceilDur(e - float64(now-st.A))
	}
	// Wait for the next window, where this window's count decays instead:
	// B × (P - e) / P + n ≤ L.
	e := max(period-float64(p.Limit-n)*period/float64(st.B), 0)
	return ceilDur(float64(st.A+int64(p.Period)-now) + e)
}

func slidingWindowCommit(st *State, n int64) { st.B += n }
