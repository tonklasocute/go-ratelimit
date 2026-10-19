package algorithm

import (
	"math/rand"
	"testing"
	"time"
)

func TestGCRA(t *testing.T) {
	// Emission interval T = 100ms, tolerance = 5 cells.
	p := Params{Limit: 10, Period: time.Second, Burst: 5}
	tests := []struct {
		name  string
		steps []step
	}{
		{"burst then one cell per emission interval", []step{
			{at: 0, n: 1, allowed: true, remaining: 4, reset: 100 * ms},
			{at: 0, n: 4, allowed: true, remaining: 0, reset: 500 * ms},
			{at: 0, n: 1, allowed: false, remaining: 0, retry: 100 * ms, reset: 500 * ms},
			{at: 250 * ms, n: 1, allowed: true, remaining: 1, reset: 350 * ms},
			{at: 250 * ms, n: 2, allowed: false, remaining: 1, retry: 50 * ms, reset: 350 * ms},
			{at: 300 * ms, n: 2, allowed: true, remaining: 0, reset: 500 * ms},
		}},
		{"an idle key is fresh again", []step{
			{at: 0, n: 5, allowed: true, remaining: 0, reset: 500 * ms},
			{at: time.Hour, n: 5, allowed: true, remaining: 0, reset: 500 * ms},
		}},
		{"no window edge", []step{
			{at: 999 * ms, n: 5, allowed: true, remaining: 0, reset: 500 * ms},
			{at: time.Second, n: 1, allowed: false, remaining: 0, retry: 99 * ms, reset: 499 * ms},
			{at: 1099 * ms, n: 1, allowed: true, remaining: 0, reset: 500 * ms},
		}},
		{"n=0 peeks", []step{
			{at: 0, n: 0, allowed: true, remaining: 5, reset: 0},
			{at: 0, n: 3, allowed: true, remaining: 2, reset: 300 * ms},
			{at: 100 * ms, n: 0, allowed: true, remaining: 3, reset: 200 * ms},
		}},
		{"n larger than the burst is never allowed", []step{
			{at: 0, n: 6, allowed: false, remaining: 5},
		}},
		{"clock going backwards waits for the TAT", []step{
			{at: time.Second, n: 5, allowed: true, remaining: 0, reset: 500 * ms},
			{at: 500 * ms, n: 1, allowed: false, remaining: 0, retry: 600 * ms, reset: time.Second},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runSteps(t, GCRA, p, tt.steps) })
	}
}

func TestGCRAStateIsOneTimestamp(t *testing.T) {
	st := runSteps(t, GCRA, Params{Limit: 10, Period: time.Second, Burst: 5}, []step{
		{at: 0, n: 2, allowed: true, remaining: 3, reset: 200 * ms},
	})
	if want := t0 + int64(200*ms); st.A != want || st.B != 0 || st.C != 0 || st.F != 0 || st.Log != nil {
		t.Fatalf("state = %+v, want only A = TAT", st)
	}
}

// GCRA is a token bucket stored as its theoretical arrival time. With an
// emission interval of whole nanoseconds the decisions are identical.
func TestGCRAMatchesTokenBucket(t *testing.T) {
	p := Params{Limit: 8, Period: time.Second, Burst: 3}
	rng := rand.New(rand.NewSource(2))
	var gcra, token State
	now := t0
	for i := 0; i < 20000; i++ {
		now += rng.Int63n(int64(400*ms)) / int64(time.Microsecond) * int64(time.Microsecond)
		n := rng.Int63n(p.Burst + 1)
		dg := Take(GCRA, &gcra, p, now, n)
		dt := Take(TokenBucket, &token, p, now, n)
		if dg != dt {
			t.Fatalf("request %d at +%v n=%d:\ngcra  %+v\ntoken %+v", i, time.Duration(now-t0), n, dg, dt)
		}
	}
}
