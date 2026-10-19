package algorithm

import (
	"testing"
	"time"
)

func TestSlidingLog(t *testing.T) {
	p := Params{Limit: 3, Period: time.Second}
	tests := []struct {
		name  string
		steps []step
	}{
		{"each entry expires one period after it was logged", []step{
			{at: 0, n: 1, allowed: true, remaining: 2, reset: time.Second},
			{at: 400 * ms, n: 1, allowed: true, remaining: 1, reset: time.Second},
			{at: 800 * ms, n: 1, allowed: true, remaining: 0, reset: time.Second},
			{at: 900 * ms, n: 1, allowed: false, remaining: 0, retry: 100 * ms, reset: 900 * ms},
			{at: time.Second, n: 1, allowed: true, remaining: 0, reset: time.Second},
			{at: time.Second, n: 1, allowed: false, remaining: 0, retry: 400 * ms, reset: time.Second},
		}},
		{"no double burst at the window edge", []step{
			{at: 999 * ms, n: 3, allowed: true, remaining: 0, reset: time.Second},
			{at: time.Second, n: 1, allowed: false, remaining: 0, retry: 999 * ms, reset: 999 * ms},
			{at: 1999 * ms, n: 3, allowed: true, remaining: 0, reset: time.Second},
		}},
		{"AllowN waits for enough entries to expire", []step{
			{at: 0, n: 1, allowed: true, remaining: 2, reset: time.Second},
			{at: 300 * ms, n: 1, allowed: true, remaining: 1, reset: time.Second},
			{at: 500 * ms, n: 3, allowed: false, remaining: 1, retry: 800 * ms, reset: 800 * ms},
			{at: 1300 * ms, n: 3, allowed: true, remaining: 0, reset: time.Second},
		}},
		{"n=0 peeks", []step{
			{at: 0, n: 0, allowed: true, remaining: 3, reset: 0},
			{at: 0, n: 2, allowed: true, remaining: 1, reset: time.Second},
			{at: 250 * ms, n: 0, allowed: true, remaining: 1, reset: 750 * ms},
		}},
		{"n larger than the limit is never allowed", []step{
			{at: 0, n: 4, allowed: false, remaining: 3},
		}},
		{"clock going backwards keeps the log sorted", []step{
			{at: 500 * ms, n: 1, allowed: true, remaining: 2, reset: time.Second},
			{at: 200 * ms, n: 1, allowed: true, remaining: 1, reset: 1300 * ms},
			{at: 1300 * ms, n: 2, allowed: true, remaining: 0, reset: time.Second},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runSteps(t, SlidingLog, p, tt.steps) })
	}
}

func TestSlidingLogMemoryIsBounded(t *testing.T) {
	p := Params{Limit: 5, Period: time.Second}
	var st State
	for i := int64(0); i < 1000; i++ {
		Take(SlidingLog, &st, p, t0+i*int64(50*ms), 1)
		if len(st.Log) > int(p.Limit) {
			t.Fatalf("log grew to %d entries", len(st.Log))
		}
	}
}
