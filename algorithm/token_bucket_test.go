package algorithm

import (
	"testing"
	"time"
)

func TestTokenBucket(t *testing.T) {
	// One token every 100ms, at most 5 stored.
	p := Params{Limit: 10, Period: time.Second, Burst: 5}
	tests := []struct {
		name  string
		steps []step
	}{
		{"starts full, refills continuously", []step{
			{at: 0, n: 1, allowed: true, remaining: 4, reset: 100 * ms},
			{at: 0, n: 4, allowed: true, remaining: 0, reset: 500 * ms},
			{at: 0, n: 1, allowed: false, remaining: 0, retry: 100 * ms, reset: 500 * ms},
			{at: 250 * ms, n: 1, allowed: true, remaining: 1, reset: 350 * ms},
			{at: 250 * ms, n: 2, allowed: false, remaining: 1, retry: 50 * ms, reset: 350 * ms},
			{at: 300 * ms, n: 2, allowed: true, remaining: 0, reset: 500 * ms},
		}},
		{"refill is capped at the burst", []step{
			{at: 0, n: 5, allowed: true, remaining: 0, reset: 500 * ms},
			{at: time.Hour, n: 5, allowed: true, remaining: 0, reset: 500 * ms},
			{at: time.Hour, n: 1, allowed: false, remaining: 0, retry: 100 * ms, reset: 500 * ms},
		}},
		{"no window edge: steady rate after the burst", []step{
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
		{"clock going backwards waits for the last refill time", []step{
			{at: time.Second, n: 5, allowed: true, remaining: 0, reset: 500 * ms},
			{at: 500 * ms, n: 1, allowed: false, remaining: 0, retry: 600 * ms, reset: time.Second},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runSteps(t, TokenBucket, p, tt.steps) })
	}
}
