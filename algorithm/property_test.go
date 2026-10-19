package algorithm

import (
	"fmt"
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"
)

// event is an allowed request of weight n at time at.
type event struct{ at, n int64 }

// traffic returns bursty request times over d: idle gaps, steady trickles,
// spikes of simultaneous requests and spikes straddling window boundaries.
func traffic(rng *rand.Rand, d time.Duration, period time.Duration) []int64 {
	var out []int64
	now, end := t0, t0+int64(d)
	for now < end {
		switch rng.Intn(4) {
		case 0: // idle
			now += rng.Int63n(int64(2 * period))
		case 1: // steady trickle
			gap := 1 + rng.Int63n(int64(period)/5)
			for i := 0; i < 50; i++ {
				now += gap
				out = append(out, now)
			}
		case 2: // spike at one instant
			for i := 0; i < 40; i++ {
				out = append(out, now)
			}
			now++
		case 3: // spike straddling the next window boundary
			edge := now - now%int64(period) + int64(period)
			for i := int64(-20); i < 20; i++ {
				out = append(out, edge+i*int64(time.Microsecond))
			}
			now = edge + int64(20*time.Microsecond)
		}
	}
	return out
}

// maxInWindow returns the largest weight of events in any interval
// (t-period, t].
func maxInWindow(events []event, period int64) int64 {
	var best, sum int64
	i := 0
	for j := range events {
		sum += events[j].n
		for events[i].at <= events[j].at-period {
			sum -= events[i].n
			i++
		}
		best = max(best, sum)
	}
	return best
}

// maxPerAlignedWindow returns the largest weight in any epoch-aligned window.
func maxPerAlignedWindow(events []event, period int64) int64 {
	counts := map[int64]int64{}
	var best int64
	for _, e := range events {
		w := e.at - e.at%period
		counts[w] += e.n
		best = max(best, counts[w])
	}
	return best
}

// maxBucketExcess returns max over intervals [t_i, t_j] of
// allowed - rate×(t_j - t_i), which a bucket bounds by its burst.
func maxBucketExcess(events []event, p Params) float64 {
	rate := float64(p.Limit) / float64(p.Period)
	best, cum, minPrefix := 0.0, 0.0, math.Inf(1)
	for _, e := range events {
		at := rate * float64(e.at-t0)
		minPrefix = min(minPrefix, cum-at) // intervals starting at this event
		cum += float64(e.n)
		best = max(best, cum-at-minPrefix)
	}
	return best
}

func TestPropertyNeverExceedsTheoreticalMaximum(t *testing.T) {
	params := []Params{
		{Limit: 10, Period: time.Second, Burst: 10},
		{Limit: 7, Period: time.Second, Burst: 3},
		{Limit: 100, Period: time.Minute, Burst: 20},
	}
	for _, id := range All {
		for pi, p := range params {
			t.Run(fmt.Sprintf("%v/%d", id, pi), func(t *testing.T) {
				rng := rand.New(rand.NewSource(int64(id)*100 + int64(pi)))
				var st State
				var allowed []event
				for _, at := range traffic(rng, 2*time.Hour, p.Period) {
					n := 1 + rng.Int63n(min(3, p.Capacity(id)))
					if d := Take(id, &st, p, at, n); d.Allowed {
						allowed = append(allowed, event{at, n})
					}
				}
				if len(allowed) == 0 {
					t.Fatal("nothing was allowed")
				}
				period := int64(p.Period)
				switch id {
				case FixedWindow, SlidingWindow:
					if got := maxPerAlignedWindow(allowed, period); got > p.Limit {
						t.Errorf("aligned window holds %d > limit %d", got, p.Limit)
					}
					got := maxInWindow(allowed, period)
					if got > 2*p.Limit {
						t.Errorf("rolling window holds %d > 2×limit %d", got, 2*p.Limit)
					}
					// The edge effect is real, which also shows the traffic
					// pattern is sharp enough to catch a broken limiter.
					if id == FixedWindow && got <= p.Limit {
						t.Errorf("expected a window edge burst above %d, got %d", p.Limit, got)
					}
				case SlidingLog:
					if got := maxInWindow(allowed, period); got > p.Limit {
						t.Errorf("rolling window holds %d > limit %d", got, p.Limit)
					}
				default:
					if got := maxBucketExcess(allowed, p); got > float64(p.Burst)+1e-6 {
						t.Errorf("allowed exceeds burst + rate×d by %v > burst %d", got, p.Burst)
					}
				}
			})
		}
	}
}

func clone(st State) State {
	st.Log = slices.Clone(st.Log)
	return st
}

// After a denial, waiting RetryAfter is enough; Peek never consumes; and
// Remaining is exact: Remaining more fit right away, one more does not.
func TestPropertyDecisionsAreConsistent(t *testing.T) {
	p := Params{Limit: 9, Period: time.Second, Burst: 4}
	for _, id := range All {
		t.Run(id.String(), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(id)))
			var st State
			now := t0
			for i := 0; i < 5000; i++ {
				now += rng.Int63n(int64(150 * ms))
				n := 1 + rng.Int63n(p.Capacity(id))

				before := clone(st)
				peek1 := Peek(id, &before, p, now, n)
				peek2 := Peek(id, &before, p, now, n)
				if peek1 != peek2 {
					t.Fatalf("Peek is not idempotent: %+v then %+v", peek1, peek2)
				}

				d := Take(id, &st, p, now, n)
				if !d.Allowed {
					if d.RetryAfter <= 0 {
						t.Fatalf("denied with RetryAfter %v", d.RetryAfter)
					}
					later := clone(st)
					if r := Peek(id, &later, p, now+int64(d.RetryAfter), n); !r.Allowed {
						t.Fatalf("request %d n=%d still denied after RetryAfter %v: %+v", i, n, d.RetryAfter, r)
					}
					continue
				}
				if d.RetryAfter != 0 {
					t.Fatalf("allowed with RetryAfter %v", d.RetryAfter)
				}
				if d.Remaining > 0 {
					probe := clone(st)
					if r := Peek(id, &probe, p, now, d.Remaining); !r.Allowed {
						t.Fatalf("Remaining %d but %d do not fit: %+v", d.Remaining, d.Remaining, r)
					}
				}
				probe := clone(st)
				if d.Remaining+1 <= p.Capacity(id) {
					if r := Peek(id, &probe, p, now, d.Remaining+1); r.Allowed {
						t.Fatalf("Remaining %d but %d fit", d.Remaining, d.Remaining+1)
					}
				}
			}
		})
	}
}

// Once ResetAfter has passed without traffic, the state behaves as new.
func TestPropertyResetAfterRestoresFullCapacity(t *testing.T) {
	p := Params{Limit: 6, Period: time.Second, Burst: 6}
	for _, id := range All {
		t.Run(id.String(), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(id) + 50))
			var st State
			now := t0
			for i := 0; i < 2000; i++ {
				now += rng.Int63n(int64(200 * ms))
				d := Take(id, &st, p, now, 1+rng.Int63n(2))
				later := clone(st)
				full := Peek(id, &later, p, now+int64(d.ResetAfter), p.Capacity(id))
				if !full.Allowed {
					t.Fatalf("request %d: not full after ResetAfter %v: %+v", i, d.ResetAfter, full)
				}
			}
		})
	}
}

func TestIDs(t *testing.T) {
	for _, id := range All {
		if !id.Valid() || id.String() == "" {
			t.Errorf("%d invalid", id)
		}
	}
	if ID(0).Valid() || ID(7).Valid() || ID(7).String() != "algorithm(7)" {
		t.Error("out of range IDs must be invalid")
	}
	for _, id := range []ID{0, 7} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Peek(%d) did not panic", id)
				}
			}()
			Peek(id, &State{}, Params{Limit: 1, Period: time.Second, Burst: 1}, t0, 1)
		}()
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Commit(%d) did not panic", id)
				}
			}()
			Commit(id, &State{}, Params{Limit: 1, Period: time.Second, Burst: 1}, t0, 1)
		}()
	}
}
