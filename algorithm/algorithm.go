// Package algorithm implements the rate limiting algorithms used by
// go-ratelimit as pure, allocation-free state machines.
//
// Every algorithm works on a small [State] value and is split into two
// steps so that several rules can be applied atomically (all-or-nothing):
//
//	d := algorithm.Peek(id, &st, p, now, n) // normalize st to now, decide
//	if d.Allowed {
//		algorithm.Commit(id, &st, p, now, n) // consume n
//	}
//
// Peek may rewrite st, but only in ways that do not change its meaning
// (expiring old windows, refilling buckets), so a denied request leaves the
// limit exactly where it was. Stores keep one State per key and rule and call
// these functions under a lock; the Redis store implements the same formulas
// in Lua.
//
// All times are Unix nanoseconds and must be positive.
package algorithm

import (
	"fmt"
	"math"
	"time"
)

// ID identifies an algorithm. The numeric values are stable: they are shared
// with the Redis Lua script.
type ID uint8

// The supported algorithms. See the file of each algorithm for a diagram
// and its trade-offs.
const (
	FixedWindow   ID = 1 // fixed window counter
	SlidingLog    ID = 2 // sliding window log
	SlidingWindow ID = 3 // sliding window counter (weighted)
	TokenBucket   ID = 4 // token bucket
	LeakyBucket   ID = 5 // leaky bucket as a meter
	GCRA          ID = 6 // generic cell rate algorithm
)

// All lists every algorithm in declaration order.
var All = []ID{FixedWindow, SlidingLog, SlidingWindow, TokenBucket, LeakyBucket, GCRA}

var names = [...]string{
	FixedWindow:   "fixed_window",
	SlidingLog:    "sliding_log",
	SlidingWindow: "sliding_window",
	TokenBucket:   "token_bucket",
	LeakyBucket:   "leaky_bucket",
	GCRA:          "gcra",
}

// String returns the snake_case name of the algorithm.
func (id ID) String() string {
	if id.Valid() {
		return names[id]
	}
	return fmt.Sprintf("algorithm(%d)", uint8(id))
}

// Valid reports whether id is a known algorithm.
func (id ID) Valid() bool { return id >= FixedWindow && id <= GCRA }

// Windowed reports whether the algorithm counts requests in windows. Windowed
// algorithms ignore Params.Burst: their capacity is Params.Limit.
func (id ID) Windowed() bool { return id == FixedWindow || id == SlidingLog || id == SlidingWindow }

// Params describe one rule: at most Limit events per Period.
type Params struct {
	Limit  int64         // events allowed per Period
	Period time.Duration // length of the window, or the time to refill Limit tokens
	Burst  int64         // bucket capacity for TokenBucket, LeakyBucket and GCRA
}

// Capacity is the largest n a single request may ask for.
func (p Params) Capacity(id ID) int64 {
	if id.Windowed() {
		return p.Limit
	}
	return p.Burst
}

// State is the per-key state of one rule. Each algorithm uses a subset of the
// fields; the zero value is a fresh, unused limit.
//
//	algorithm       A                 B              C           F       Log
//	FixedWindow     window start      count          -           -       -
//	SlidingLog      -                 -              -           -       timestamps
//	SlidingWindow   window start      current count  prev count  -       -
//	TokenBucket     last refill       -              -           tokens  -
//	LeakyBucket     last leak         -              -           level   -
//	GCRA            TAT               -              -           -       -
type State struct {
	A, B, C int64
	F       float64
	Log     []int64
}

// Decision is the outcome of applying an algorithm.
type Decision struct {
	Allowed bool
	// Limit is the capacity of the rule (Limit or Burst, see Params.Capacity).
	Limit int64
	// Remaining is how many more events fit right now, after this request if
	// it was allowed.
	Remaining int64
	// ResetAfter is the time until the state is fresh again: the end of the
	// window, or until the bucket is full. Stores use it as the key TTL.
	ResetAfter time.Duration
	// RetryAfter is how long to wait before the same request would be
	// allowed. It is 0 when the request was allowed.
	RetryAfter time.Duration
}

// eps absorbs floating point error in the algorithms that use float64.
const eps = 1e-9

// Peek normalizes st to now and decides whether n events would be allowed,
// without consuming them. For an allowed decision Remaining and ResetAfter
// describe the state after Commit. n must be in [0, Capacity].
func Peek(id ID, st *State, p Params, now int64, n int64) Decision {
	switch id {
	case FixedWindow:
		return fixedWindowPeek(st, p, now, n)
	default:
		panic("algorithm: unknown algorithm " + id.String())
	}
}

// Commit consumes n events. It must follow an allowed Peek with the same
// arguments.
func Commit(id ID, st *State, p Params, now int64, n int64) {
	switch id {
	case FixedWindow:
		fixedWindowCommit(st, n)
	default:
		panic("algorithm: unknown algorithm " + id.String())
	}
}

// Take is Peek followed by Commit when the request is allowed.
func Take(id ID, st *State, p Params, now int64, n int64) Decision {
	d := Peek(id, st, p, now, n)
	if d.Allowed && n > 0 {
		Commit(id, st, p, now, n)
	}
	return d
}

// ceilDur converts a float number of nanoseconds to a Duration, rounding up
// so that waiting RetryAfter is always enough.
func ceilDur(ns float64) time.Duration {
	if ns <= 0 {
		return 0
	}
	if ns >= math.MaxInt64 {
		return math.MaxInt64
	}
	return time.Duration(math.Ceil(ns - eps))
}

// floorInt converts a float count to int64, rounding down and never below 0.
func floorInt(v float64) int64 {
	if v <= 0 {
		return 0
	}
	return int64(math.Floor(v + eps))
}
