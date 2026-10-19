package ratelimit

import "context"

// Option configures a limiter created by New. A Rate is also an Option.
type Option interface {
	apply(*config)
}

type config struct {
	rate      *Rate
	burst     int
	algorithm Algorithm
	name      string
	rules     []Rule
	store     Store
	clock     Clock
	policy    FailurePolicy
	onError   func(ctx context.Context, key string, err error)
}

type optionFunc func(*config)

func (f optionFunc) apply(c *config) { f(c) }

// WithBurst sets the burst (bucket capacity) of the primary rule. It only
// affects TokenBucket, LeakyBucket and GCRA and defaults to the rate limit.
func WithBurst(n int) Option { return optionFunc(func(c *config) { c.burst = n }) }

// WithAlgorithm sets the algorithm of the primary rule. The default is GCRA.
func WithAlgorithm(a Algorithm) Option { return optionFunc(func(c *config) { c.algorithm = a }) }

// WithName sets the name of the primary rule. See Rule.Name.
func WithName(name string) Option { return optionFunc(func(c *config) { c.name = name }) }

// WithRules adds rules that are enforced together with the primary rate.
func WithRules(rules ...Rule) Option {
	return optionFunc(func(c *config) { c.rules = append(c.rules, rules...) })
}

// WithStore sets where limiter state is kept. It is required.
func WithStore(s Store) Option { return optionFunc(func(c *config) { c.store = s }) }

// WithClock sets the clock used to timestamp requests. Stores that keep their
// own time, such as Redis with server time, ignore it.
func WithClock(clk Clock) Option { return optionFunc(func(c *config) { c.clock = clk }) }

// WithFailurePolicy sets what happens when the store returns an error.
// The default is FailOpen.
func WithFailurePolicy(p FailurePolicy) Option { return optionFunc(func(c *config) { c.policy = p }) }

// WithOnError registers a hook called with every store error, before the
// failure policy is applied. Use it for logging and metrics.
func WithOnError(fn func(ctx context.Context, key string, err error)) Option {
	return optionFunc(func(c *config) { c.onError = fn })
}
