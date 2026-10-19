package algorithm

import (
	"testing"
	"time"
)

// t0 is a whole second, so windows start at t0 + k*Period for round periods.
var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()

// step is one request at offset at from t0 and the expected decision.
type step struct {
	at        time.Duration
	n         int64
	allowed   bool
	remaining int64
	retry     time.Duration
	reset     time.Duration
}

// runSteps applies steps in order to a fresh state and checks each decision.
func runSteps(t *testing.T, id ID, p Params, steps []step) *State {
	t.Helper()
	var st State
	for i, s := range steps {
		d := Take(id, &st, p, t0+int64(s.at), s.n)
		want := Decision{Allowed: s.allowed, Limit: p.Capacity(id), Remaining: s.remaining, RetryAfter: s.retry, ResetAfter: s.reset}
		if d != want {
			t.Fatalf("step %d (at %v, n=%d):\n got %+v\nwant %+v", i, s.at, s.n, d, want)
		}
	}
	return &st
}

const ms = time.Millisecond
