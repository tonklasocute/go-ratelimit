package algorithm

import (
	"testing"
	"time"
)

func TestSlidingWindow(t *testing.T) {
	p := Params{Limit: 10, Period: time.Second}
	tests := []struct {
		name  string
		steps []step
	}{
		{"previous window is weighted by its overlap", []step{
			{at: 0, n: 10, allowed: true, remaining: 0, reset: 2 * time.Second},
			// Still in window 0: the count must roll into window 1 and decay
			// to 9 there: 1s - 500ms + 100ms.
			{at: 500 * ms, n: 1, allowed: false, remaining: 0, retry: 600 * ms, reset: 1500 * ms},
			// Window 1 at 10%: estimate = 10×0.9 + 0 = 9.
			{at: 1100 * ms, n: 1, allowed: true, remaining: 0, reset: 1900 * ms},
			// estimate = 9 + 1 = 10; wait until 10×w + 1 + 1 ≤ 10, w = 0.8.
			{at: 1100 * ms, n: 1, allowed: false, remaining: 0, retry: 100 * ms, reset: 1900 * ms},
			{at: 1200 * ms, n: 1, allowed: true, remaining: 0, reset: 1800 * ms},
			{at: 1600 * ms, n: 4, allowed: true, remaining: 0, reset: 1400 * ms},
		}},
		{"smooths the window edge", []step{
			{at: 999 * ms, n: 10, allowed: true, remaining: 0, reset: 1001 * ms},
			{at: time.Second, n: 1, allowed: false, remaining: 0, retry: 100 * ms, reset: time.Second},
			{at: 1500 * ms, n: 5, allowed: true, remaining: 0, reset: 1500 * ms},
		}},
		{"skipped windows forget the past", []step{
			{at: 0, n: 10, allowed: true, remaining: 0, reset: 2 * time.Second},
			{at: 2500 * ms, n: 1, allowed: true, remaining: 9, reset: 1500 * ms},
		}},
		{"n=0 peeks", []step{
			{at: 0, n: 0, allowed: true, remaining: 10, reset: 0},
			{at: 0, n: 4, allowed: true, remaining: 6, reset: 2 * time.Second},
			{at: 1500 * ms, n: 0, allowed: true, remaining: 8, reset: 500 * ms},
		}},
		{"n larger than the limit is never allowed", []step{
			{at: 0, n: 11, allowed: false, remaining: 10},
		}},
		{"clock going backwards stays in the newest window", []step{
			{at: 2 * time.Second, n: 10, allowed: true, remaining: 0, reset: 2 * time.Second},
			{at: 1500 * ms, n: 1, allowed: false, remaining: 0, retry: 1600 * ms, reset: 2500 * ms},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runSteps(t, SlidingWindow, p, tt.steps) })
	}
}
