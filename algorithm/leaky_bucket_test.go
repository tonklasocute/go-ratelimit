package algorithm

import (
	"math/rand"
	"testing"
	"time"
)

func TestLeakyBucket(t *testing.T) {
	// Drains one unit every 100ms, holds at most 5.
	p := Params{Limit: 10, Period: time.Second, Burst: 5}
	tests := []struct {
		name  string
		steps []step
	}{
		{"starts empty, drains continuously", []step{
			{at: 0, n: 1, allowed: true, remaining: 4, reset: 100 * ms},
			{at: 0, n: 4, allowed: true, remaining: 0, reset: 500 * ms},
			{at: 0, n: 1, allowed: false, remaining: 0, retry: 100 * ms, reset: 500 * ms},
			{at: 250 * ms, n: 1, allowed: true, remaining: 1, reset: 350 * ms},
			{at: 250 * ms, n: 2, allowed: false, remaining: 1, retry: 50 * ms, reset: 350 * ms},
			{at: 300 * ms, n: 2, allowed: true, remaining: 0, reset: 500 * ms},
		}},
		{"level never drops below empty", []step{
			{at: 0, n: 5, allowed: true, remaining: 0, reset: 500 * ms},
			{at: time.Hour, n: 5, allowed: true, remaining: 0, reset: 500 * ms},
		}},
		{"n=0 peeks", []step{
			{at: 0, n: 0, allowed: true, remaining: 5, reset: 0},
			{at: 0, n: 3, allowed: true, remaining: 2, reset: 300 * ms},
			{at: 100 * ms, n: 0, allowed: true, remaining: 3, reset: 200 * ms},
		}},
		{"n larger than the capacity is never allowed", []step{
			{at: 0, n: 6, allowed: false, remaining: 5},
		}},
		{"clock going backwards waits for the last leak time", []step{
			{at: time.Second, n: 5, allowed: true, remaining: 0, reset: 500 * ms},
			{at: 500 * ms, n: 1, allowed: false, remaining: 0, retry: 600 * ms, reset: time.Second},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runSteps(t, LeakyBucket, p, tt.steps) })
	}
}

// A leaky bucket used as a meter is the mirror image of a token bucket:
// level = Burst - tokens. They must make the same decisions.
func TestLeakyBucketMirrorsTokenBucket(t *testing.T) {
	p := Params{Limit: 7, Period: time.Second, Burst: 4}
	rng := rand.New(rand.NewSource(1))
	var leaky, token State
	now := t0
	for i := 0; i < 20000; i++ {
		now += rng.Int63n(int64(300 * ms))
		n := rng.Int63n(p.Burst + 1)
		dl := Take(LeakyBucket, &leaky, p, now, n)
		dt := Take(TokenBucket, &token, p, now, n)
		if dl != dt {
			t.Fatalf("request %d at +%v n=%d:\nleaky %+v\ntoken %+v", i, time.Duration(now-t0), n, dl, dt)
		}
	}
}
