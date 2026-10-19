package algorithm

import "time"

// Fixed window counter
//
// Time is cut into windows of length Period aligned to the Unix epoch, and
// each window has its own counter. A request is allowed while the counter of
// the current window is below Limit; the counter starts at zero again when
// the next window begins.
//
//	Limit = 3, Period = 1s                  x = allowed   - = denied
//
//	window   |  [0s, 1s)   |  [1s, 2s)   |  [2s, 3s)   |
//	requests | x x x - -   |          xxx|x x x -      |
//	count    | 1 2 3 3 3   |          123|1 2 3 3      |
//	                                    ^^^ ^^^
//	         6 requests within ~2ms around the 2s boundary
//
// Memory per key: two integers (window start, count).
//
// Accuracy at window edges: poor. A client can send Limit requests at the end
// of one window and Limit more at the start of the next, so up to 2×Limit
// requests can pass within one Period.
//
// Bursts: up to Limit at any moment, 2×Limit across an edge. Every client's
// window resets at the same instant, which can cause synchronized spikes
// ("thundering herd") at window starts.
//
// Distributed-friendliness: excellent. In Redis it is an INCR plus EXPIRE on a
// key named after the window; here it is a hash so that it composes with the
// other rules in one atomic script.

func fixedWindowPeek(st *State, p Params, now, n int64) Decision {
	period := int64(p.Period)
	if start := now - now%period; start > st.A {
		st.A, st.B = start, 0
	}
	reset := time.Duration(st.A + period - now)
	d := Decision{Limit: p.Limit, ResetAfter: reset}
	if st.B+n <= p.Limit {
		d.Allowed = true
		d.Remaining = p.Limit - st.B - n
		return d
	}
	d.Remaining = p.Limit - st.B
	if n <= p.Limit {
		d.RetryAfter = reset
	}
	return d
}

func fixedWindowCommit(st *State, n int64) { st.B += n }
