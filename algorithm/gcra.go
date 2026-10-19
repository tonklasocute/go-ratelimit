package algorithm

import "time"

// GCRA — generic cell rate algorithm
//
// GCRA comes from ATM networks. Instead of counting tokens it stores one
// timestamp per key: the theoretical arrival time (TAT) at which the next
// request would be on schedule if requests arrived exactly at the sustained
// rate. With emission interval T = Period/Limit and a tolerance of Burst×T:
//
//	newTAT  = max(TAT, now) + n×T
//	allowAt = newTAT - Burst×T
//	allow if now ≥ allowAt, then TAT = newTAT
//
//	Limit = 10/s (T = 100ms), Burst = 5 (tolerance 500ms)
//
//	        now                       TAT after 3 requests
//	         │◄───── 300ms ahead ─────►│
//	time ────┼─────────────────────────┼────────────┼──────►
//	         │◄──────────── tolerance 500ms ───────►│
//	         room for 2 more requests (200ms / T)
//
// The TAT running ahead of now by at most the tolerance is exactly a token
// bucket (and a leaky bucket meter) with capacity Burst, stored as a single
// number. It is the algorithm used by redis_rate, throttled and Stripe.
//
// Memory per key: one timestamp (8 bytes), the smallest of all algorithms.
//
// Accuracy at window edges: no windows; at most Burst + d×rate requests in
// any interval d.
//
// Bursts: up to Burst at once, then requests are spaced T apart.
//
// Distributed-friendliness: excellent. One GET and one SET in Redis, the
// state expires naturally at TAT, and there is no float drift. It needs a
// shared clock; the Redis store uses the server's TIME.
//
// This is the default algorithm.

// gcraInterval is the emission interval T, rounded up so that the rate is
// never exceeded.
func gcraInterval(p Params) int64 {
	return (int64(p.Period) + p.Limit - 1) / p.Limit
}

func gcraPeek(st *State, p Params, now, n int64) Decision {
	interval := gcraInterval(p)
	tolerance := p.Burst * interval
	tat := max(st.A, now)
	newTAT := tat + n*interval
	allowAt := newTAT - tolerance

	d := Decision{Limit: p.Burst}
	if now >= allowAt {
		d.Allowed = true
		d.Remaining = (now - allowAt) / interval
		d.ResetAfter = time.Duration(newTAT - now)
		return d
	}
	if n <= p.Burst {
		d.RetryAfter = time.Duration(allowAt - now)
	}
	d.Remaining = max(now-(tat-tolerance), 0) / interval
	d.ResetAfter = time.Duration(tat - now)
	return d
}

func gcraCommit(st *State, p Params, now, n int64) {
	st.A = max(st.A, now) + n*gcraInterval(p)
}
