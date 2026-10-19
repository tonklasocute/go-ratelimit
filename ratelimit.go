package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Limiter decides whether requests identified by a key may proceed.
//
// Implementations must be safe for concurrent use.
type Limiter interface {
	// Allow is AllowN(ctx, key, 1).
	Allow(ctx context.Context, key string) (Result, error)
	// AllowN reports whether n events may happen now for key and, if so,
	// consumes them. n == 0 reports the current state without consuming.
	AllowN(ctx context.Context, key string, n int) (Result, error)
	// Reset forgets everything recorded for key.
	Reset(ctx context.Context, key string) error
}

// Result describes a rate limit decision.
type Result struct {
	Allowed bool
	// Limit is the capacity of the rule: requests per window for windowed
	// algorithms, the burst size for bucket algorithms.
	Limit int
	// Remaining is how many more requests fit right now.
	Remaining int
	// ResetAfter is the time until the limit is fully available again: the
	// end of the current window, or until the bucket is full.
	ResetAfter time.Duration
	// RetryAfter is how long to wait before retrying. It is 0 if allowed.
	RetryAfter time.Duration
	// Policy is the name of the rule that produced this result. For a
	// limiter with several rules it is the most restrictive one.
	Policy string
}

var (
	// ErrStore wraps errors returned by a Store when the limiter is
	// configured to fail closed.
	ErrStore = errors.New("ratelimit: store error")
	// ErrExceedsLimit is returned by AllowN when n is larger than the
	// capacity of a rule, so the request could never be allowed.
	ErrExceedsLimit = errors.New("ratelimit: n exceeds limit")
	// ErrInvalidN is returned by AllowN when n is negative.
	ErrInvalidN = errors.New("ratelimit: n must not be negative")
	// ErrNoStore is returned by New when no store was configured.
	ErrNoStore = errors.New("ratelimit: no store configured, use WithStore")
	// ErrNoRules is returned by New when no rate was configured.
	ErrNoRules = errors.New("ratelimit: no rate configured")
)

// FailurePolicy decides what a limiter does when its store fails.
type FailurePolicy uint8

const (
	// FailOpen allows the request and returns a nil error. Availability is
	// preferred over protection; this is the default.
	FailOpen FailurePolicy = iota
	// FailClosed denies the request and returns an error wrapping ErrStore.
	FailClosed
)

// RuleLimiter is a Limiter that applies one or more rules to every key,
// atomically: a request is allowed only if every rule allows it, and a
// denied request consumes nothing from any rule.
type RuleLimiter struct {
	store   Store
	rules   []Rule
	clock   Clock
	policy  FailurePolicy
	onError func(ctx context.Context, key string, err error)
}

var _ Limiter = (*RuleLimiter)(nil)

// New creates a limiter. It needs a rate and a store:
//
//	lim, err := ratelimit.New(ratelimit.PerSecond(10), ratelimit.WithStore(store))
//
// More rules can be added with WithRules; the result of a check is then the
// most restrictive one.
func New(opts ...Option) (*RuleLimiter, error) {
	var c config
	for _, o := range opts {
		o.apply(&c)
	}
	rules := c.rules
	if c.rate != nil {
		primary := Rule{Name: c.name, Rate: *c.rate, Burst: c.burst, Algorithm: c.algorithm}
		rules = append([]Rule{primary}, rules...)
	}
	if len(rules) == 0 {
		return nil, ErrNoRules
	}
	if c.store == nil {
		return nil, ErrNoStore
	}
	seen := make(map[string]bool, len(rules))
	for i := range rules {
		r, err := rules[i].normalize()
		if err != nil {
			return nil, err
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("ratelimit: duplicate rule name %q", r.Name)
		}
		seen[r.Name] = true
		rules[i] = r
	}
	if c.clock == nil {
		c.clock = SystemClock()
	}
	return &RuleLimiter{
		store:   c.store,
		rules:   rules,
		clock:   c.clock,
		policy:  c.policy,
		onError: c.onError,
	}, nil
}

// NewComposite creates a limiter enforcing all rules, for example 10 per
// second and 1000 per hour. It is New with WithRules(rules...).
func NewComposite(rules []Rule, opts ...Option) (*RuleLimiter, error) {
	return New(append([]Option{WithRules(rules...)}, opts...)...)
}

// Allow is AllowN(ctx, key, 1).
func (l *RuleLimiter) Allow(ctx context.Context, key string) (Result, error) {
	return l.AllowN(ctx, key, 1)
}

// AllowN reports whether n events may happen now for key.
func (l *RuleLimiter) AllowN(ctx context.Context, key string, n int) (Result, error) {
	if n < 0 {
		return Result{}, ErrInvalidN
	}
	for i := range l.rules {
		if c := l.rules[i].Capacity(); n > c {
			return Result{Limit: c, Policy: l.rules[i].Name}, ErrExceedsLimit
		}
	}
	res, err := l.store.Take(ctx, key, l.rules, n, l.clock.Now())
	if err != nil {
		return l.fail(ctx, key, err)
	}
	return res, nil
}

// Reset forgets everything recorded for key under this limiter's rules.
func (l *RuleLimiter) Reset(ctx context.Context, key string) error {
	return l.store.Reset(ctx, key, l.rules)
}

// Rules returns the normalized rules of the limiter. Middleware uses it to
// render the RateLimit-Policy header.
func (l *RuleLimiter) Rules() []Rule {
	return append([]Rule(nil), l.rules...)
}

func (l *RuleLimiter) fail(ctx context.Context, key string, err error) (Result, error) {
	if l.onError != nil {
		l.onError(ctx, key, err)
	}
	r := l.rules[0]
	res := Result{Limit: r.Capacity(), Policy: r.Name}
	if l.policy == FailClosed {
		return res, fmt.Errorf("%w: %w", ErrStore, err)
	}
	res.Allowed = true
	res.Remaining = res.Limit
	return res, nil
}
