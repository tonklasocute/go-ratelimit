package algorithm

import (
	"slices"
	"time"
)

// Sliding window log
//
// Keep the timestamp of every allowed request. A request is allowed if fewer
// than Limit timestamps lie within the last Period; timestamps older than
// that are dropped. The window slides with each request instead of jumping.
//
//	Limit = 3, Period = 1s
//
//	log         0.0s  0.4s  0.8s
//	                              0.9s: window (-0.1s, 0.9s] holds 3 → deny
//	                                    retry when 0.0s expires, at 1.0s
//	                              1.0s: window (0.0s, 1.0s] holds 2 → allow
//	            ├─────────── 1s ───────────┤
//	                ├─────────── 1s ───────────┤
//
// Memory per key: one timestamp per allowed request, up to Limit (8 bytes
// each). Expensive for large limits such as 10 000/hour.
//
// Accuracy at window edges: exact. No Period ever contains more than Limit
// allowed requests.
//
// Bursts: up to Limit at once, after which requests trickle in as old ones
// expire.
//
// Distributed-friendliness: good but heavy. In Redis it is a sorted set
// (ZREMRANGEBYSCORE + ZCARD + ZADD); memory and work grow with Limit.

func slidingLogPeek(st *State, p Params, now, n int64) Decision {
	period := int64(p.Period)
	cut := now - period
	expired := 0
	for expired < len(st.Log) && st.Log[expired] <= cut {
		expired++
	}
	if expired > 0 {
		st.Log = st.Log[:copy(st.Log, st.Log[expired:])]
	}
	count := int64(len(st.Log))
	d := Decision{Limit: p.Limit}
	if count > 0 {
		d.ResetAfter = time.Duration(st.Log[count-1] + period - now)
	}
	if count+n <= p.Limit {
		d.Allowed = true
		d.Remaining = p.Limit - count - n
		if n > 0 {
			d.ResetAfter = max(d.ResetAfter, p.Period)
		}
		return d
	}
	d.Remaining = p.Limit - count
	if n <= p.Limit {
		// The k-th oldest entry must expire before n more fit.
		k := count + n - p.Limit
		d.RetryAfter = time.Duration(st.Log[k-1] + period - now)
	}
	return d
}

func slidingLogCommit(st *State, now, n int64) {
	for i := int64(0); i < n; i++ {
		st.Log = append(st.Log, now)
	}
	if last := len(st.Log) - int(n) - 1; last >= 0 && st.Log[last] > now {
		slices.Sort(st.Log) // the clock went backwards
	}
}
