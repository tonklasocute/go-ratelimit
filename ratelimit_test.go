package ratelimit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/ratelimittest"
)

// stubStore returns a fixed result or error and records its calls.
type stubStore struct {
	res   ratelimit.Result
	err   error
	calls int
	rules []ratelimit.Rule
	n     int
	now   time.Time
	reset string
}

func (s *stubStore) Take(_ context.Context, _ string, rules []ratelimit.Rule, n int, now time.Time) (ratelimit.Result, error) {
	s.calls++
	s.rules, s.n, s.now = rules, n, now
	return s.res, s.err
}

func (s *stubStore) Reset(_ context.Context, key string, _ []ratelimit.Rule) error {
	s.reset = key
	return s.err
}

func TestRates(t *testing.T) {
	tests := []struct {
		rate ratelimit.Rate
		want string
	}{
		{ratelimit.PerSecond(10), "10/1s"},
		{ratelimit.PerMinute(60), "60/1m"},
		{ratelimit.PerHour(1000), "1000/1h"},
		{ratelimit.Per(5, 90*time.Minute), "5/1h30m"},
		{ratelimit.Per(3, 250*time.Millisecond), "3/250ms"},
	}
	for _, tt := range tests {
		if got := tt.rate.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestNewValidation(t *testing.T) {
	store := &stubStore{}
	tests := []struct {
		name string
		opts []ratelimit.Option
		want error
	}{
		{"no rate", []ratelimit.Option{ratelimit.WithStore(store)}, ratelimit.ErrNoRules},
		{"no store", []ratelimit.Option{ratelimit.PerSecond(1)}, ratelimit.ErrNoStore},
		{"zero limit", []ratelimit.Option{ratelimit.PerSecond(0), ratelimit.WithStore(store)}, nil},
		{"tiny period", []ratelimit.Option{ratelimit.Per(1, time.Nanosecond), ratelimit.WithStore(store)}, nil},
		{"negative burst", []ratelimit.Option{ratelimit.PerSecond(1), ratelimit.WithBurst(-1), ratelimit.WithStore(store)}, nil},
		{"bad algorithm", []ratelimit.Option{ratelimit.PerSecond(1), ratelimit.WithAlgorithm(99), ratelimit.WithStore(store)}, nil},
		{"duplicate names", []ratelimit.Option{
			ratelimit.PerSecond(1), ratelimit.WithStore(store),
			ratelimit.WithRules(ratelimit.Rule{Rate: ratelimit.PerSecond(1)}),
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ratelimit.New(tt.opts...)
			if err == nil {
				t.Fatal("New() succeeded, want error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("New() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRuleDefaults(t *testing.T) {
	lim, err := ratelimit.New(
		ratelimit.PerSecond(10),
		ratelimit.WithStore(&stubStore{}),
		ratelimit.WithRules(
			ratelimit.Rule{Rate: ratelimit.PerHour(1000), Algorithm: ratelimit.SlidingWindow, Burst: 5},
			ratelimit.Rule{Name: "custom", Rate: ratelimit.PerMinute(100), Algorithm: ratelimit.TokenBucket, Burst: 20},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	rules := lim.Rules()
	want := []struct {
		name     string
		alg      ratelimit.Algorithm
		capacity int
	}{
		{"gcra_10_1s", ratelimit.GCRA, 10},
		{"sliding_window_1000_1h", ratelimit.SlidingWindow, 1000}, // burst ignored
		{"custom", ratelimit.TokenBucket, 20},
	}
	if len(rules) != len(want) {
		t.Fatalf("got %d rules, want %d", len(rules), len(want))
	}
	for i, w := range want {
		r := rules[i]
		if r.Name != w.name || r.Algorithm != w.alg || r.Capacity() != w.capacity {
			t.Errorf("rule %d = {%s %v cap %d}, want {%s %v cap %d}",
				i, r.Name, r.Algorithm, r.Capacity(), w.name, w.alg, w.capacity)
		}
	}
	p := rules[2].Params()
	if p.Limit != 100 || p.Period != time.Minute || p.Burst != 20 {
		t.Errorf("Params() = %+v", p)
	}
	// Rules returns a copy.
	rules[0].Name = "changed"
	if lim.Rules()[0].Name != "gcra_10_1s" {
		t.Error("Rules() exposed internal slice")
	}
}

func TestAllowPassesClockAndN(t *testing.T) {
	clk := ratelimittest.NewClock()
	store := &stubStore{res: ratelimit.Result{Allowed: true, Limit: 5, Remaining: 4, Policy: "p"}}
	lim, err := ratelimit.New(ratelimit.PerSecond(5), ratelimit.WithStore(store), ratelimit.WithClock(clk))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := lim.Allow(ctx, "k")
	if err != nil || res != store.res {
		t.Fatalf("Allow() = %+v, %v", res, err)
	}
	if store.n != 1 || !store.now.Equal(clk.Now()) || len(store.rules) != 1 {
		t.Fatalf("store got n=%d now=%v rules=%d", store.n, store.now, len(store.rules))
	}
	if _, err := lim.AllowN(ctx, "k", 0); err != nil || store.n != 0 {
		t.Fatalf("AllowN(0) err=%v n=%d", err, store.n)
	}
	if err := lim.Reset(ctx, "k"); err != nil || store.reset != "k" {
		t.Fatalf("Reset() err=%v key=%q", err, store.reset)
	}
}

func TestAllowNInvalid(t *testing.T) {
	store := &stubStore{}
	lim, err := ratelimit.New(ratelimit.PerSecond(5), ratelimit.WithBurst(8), ratelimit.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := lim.AllowN(ctx, "k", -1); !errors.Is(err, ratelimit.ErrInvalidN) {
		t.Fatalf("AllowN(-1) error = %v", err)
	}
	res, err := lim.AllowN(ctx, "k", 9)
	if !errors.Is(err, ratelimit.ErrExceedsLimit) || res.Allowed || res.Limit != 8 {
		t.Fatalf("AllowN(9) = %+v, %v", res, err)
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times for invalid n", store.calls)
	}
	if _, err := lim.AllowN(ctx, "k", 8); err != nil {
		t.Fatalf("AllowN(burst) error = %v", err)
	}
}

func TestFailurePolicy(t *testing.T) {
	boom := errors.New("connection refused")
	tests := []struct {
		name        string
		policy      ratelimit.FailurePolicy
		wantAllowed bool
		wantErr     bool
	}{
		{"fail open", ratelimit.FailOpen, true, false},
		{"fail closed", ratelimit.FailClosed, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hooked error
			lim, err := ratelimit.New(
				ratelimit.PerSecond(5),
				ratelimit.WithStore(&stubStore{err: boom}),
				ratelimit.WithFailurePolicy(tt.policy),
				ratelimit.WithOnError(func(_ context.Context, key string, err error) { hooked = err }),
			)
			if err != nil {
				t.Fatal(err)
			}
			res, err := lim.Allow(context.Background(), "k")
			if res.Allowed != tt.wantAllowed || (err != nil) != tt.wantErr {
				t.Fatalf("Allow() = %+v, %v", res, err)
			}
			if tt.wantErr && (!errors.Is(err, ratelimit.ErrStore) || !errors.Is(err, boom)) {
				t.Fatalf("error %v does not wrap ErrStore and cause", err)
			}
			if !errors.Is(hooked, boom) {
				t.Fatalf("OnError got %v", hooked)
			}
			if res.Limit != 5 || res.Policy != "gcra_5_1s" {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

func TestNewComposite(t *testing.T) {
	store := &stubStore{}
	lim, err := ratelimit.NewComposite([]ratelimit.Rule{
		{Rate: ratelimit.PerSecond(10)},
		{Rate: ratelimit.PerHour(1000)},
	}, ratelimit.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lim.Allow(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if len(store.rules) != 2 {
		t.Fatalf("store got %d rules, want 2", len(store.rules))
	}
}

func TestMostRestrictive(t *testing.T) {
	allow := func(rem int, reset time.Duration) ratelimit.Result {
		return ratelimit.Result{Allowed: true, Remaining: rem, ResetAfter: reset, Policy: "a"}
	}
	deny := func(retry time.Duration) ratelimit.Result {
		return ratelimit.Result{RetryAfter: retry, Policy: "d"}
	}
	tests := []struct {
		name string
		a, b ratelimit.Result
		want ratelimit.Result
	}{
		{"zero left", ratelimit.Result{}, allow(1, 0), allow(1, 0)},
		{"zero right", deny(1), ratelimit.Result{}, deny(1)},
		{"deny beats allow", allow(5, 0), deny(1), deny(1)},
		{"deny beats allow reversed", deny(1), allow(5, 0), deny(1)},
		{"longer retry", deny(1), deny(3), deny(3)},
		{"shorter retry kept", deny(3), deny(1), deny(3)},
		{"fewer remaining", allow(5, 0), allow(2, 0), allow(2, 0)},
		{"more remaining kept", allow(2, 0), allow(5, 0), allow(2, 0)},
		{"tie longer reset", allow(2, time.Second), allow(2, time.Minute), allow(2, time.Minute)},
	}
	for _, tt := range tests {
		if got := ratelimit.MostRestrictive(tt.a, tt.b); got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestSystemClock(t *testing.T) {
	before := time.Now()
	got := ratelimit.SystemClock().Now()
	if got.Before(before) || time.Since(got) > time.Second {
		t.Fatalf("SystemClock().Now() = %v", got)
	}
}

func TestFakeClock(t *testing.T) {
	clk := ratelimittest.NewClock()
	if !clk.Now().Equal(ratelimittest.Epoch) {
		t.Fatal("NewClock not at Epoch")
	}
	clk.Advance(time.Second)
	if got := clk.Now().Sub(ratelimittest.Epoch); got != time.Second {
		t.Fatalf("Advance: %v", got)
	}
	clk.Set(ratelimittest.Epoch)
	if !clk.Now().Equal(ratelimittest.Epoch) {
		t.Fatal("Set did not move the clock")
	}
	at := time.Unix(100, 0)
	if !ratelimittest.NewClockAt(at).Now().Equal(at) {
		t.Fatal("NewClockAt")
	}
}
