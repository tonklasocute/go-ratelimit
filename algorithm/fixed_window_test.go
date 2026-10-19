package algorithm

import (
	"testing"
	"time"
)

func TestFixedWindow(t *testing.T) {
	p := Params{Limit: 3, Period: time.Second}
	tests := []struct {
		name  string
		steps []step
	}{
		{"fills then denies until the window ends", []step{
			{at: 0, n: 1, allowed: true, remaining: 2, reset: time.Second},
			{at: 100 * ms, n: 1, allowed: true, remaining: 1, reset: 900 * ms},
			{at: 200 * ms, n: 1, allowed: true, remaining: 0, reset: 800 * ms},
			{at: 300 * ms, n: 1, allowed: false, remaining: 0, retry: 700 * ms, reset: 700 * ms},
			{at: 999 * ms, n: 1, allowed: false, remaining: 0, retry: ms, reset: ms},
			{at: time.Second, n: 1, allowed: true, remaining: 2, reset: time.Second},
		}},
		{"double burst at the window edge", []step{
			{at: 999 * ms, n: 3, allowed: true, remaining: 0, reset: ms},
			{at: time.Second, n: 3, allowed: true, remaining: 0, reset: time.Second},
			{at: time.Second + ms, n: 1, allowed: false, remaining: 0, retry: 999 * ms, reset: 999 * ms},
		}},
		{"AllowN partially fitting is denied without consuming", []step{
			{at: 0, n: 2, allowed: true, remaining: 1, reset: time.Second},
			{at: 0, n: 2, allowed: false, remaining: 1, retry: time.Second, reset: time.Second},
			{at: 0, n: 1, allowed: true, remaining: 0, reset: time.Second},
		}},
		{"n=0 peeks", []step{
			{at: 0, n: 0, allowed: true, remaining: 3, reset: time.Second},
			{at: 0, n: 3, allowed: true, remaining: 0, reset: time.Second},
			{at: 500 * ms, n: 0, allowed: true, remaining: 0, reset: 500 * ms},
		}},
		{"skipped windows start fresh", []step{
			{at: 0, n: 3, allowed: true, remaining: 0, reset: time.Second},
			{at: 3200 * ms, n: 3, allowed: true, remaining: 0, reset: 800 * ms},
		}},
		{"clock going backwards stays in the newest window", []step{
			{at: 2 * time.Second, n: 3, allowed: true, remaining: 0, reset: time.Second},
			{at: 1500 * ms, n: 1, allowed: false, remaining: 0, retry: 1500 * ms, reset: 1500 * ms},
		}},
		{"n larger than the limit is never allowed", []step{
			{at: 0, n: 4, allowed: false, remaining: 3, reset: time.Second},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runSteps(t, FixedWindow, p, tt.steps) })
	}
}
