package ratelimit

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tonklasocute/go-ratelimit/algorithm"
)

// Rate is a number of events per period. A Rate is also an Option that sets
// the rate of the limiter's primary rule:
//
//	ratelimit.New(ratelimit.PerSecond(10), ratelimit.WithStore(s))
type Rate struct {
	Limit  int
	Period time.Duration
}

// Per returns a rate of limit events per period.
func Per(limit int, period time.Duration) Rate { return Rate{Limit: limit, Period: period} }

// PerSecond returns a rate of limit events per second.
func PerSecond(limit int) Rate { return Per(limit, time.Second) }

// PerMinute returns a rate of limit events per minute.
func PerMinute(limit int) Rate { return Per(limit, time.Minute) }

// PerHour returns a rate of limit events per hour.
func PerHour(limit int) Rate { return Per(limit, time.Hour) }

// String formats the rate as "limit/period", for example "10/1s".
func (r Rate) String() string { return strconv.Itoa(r.Limit) + "/" + formatPeriod(r.Period) }

// formatPeriod is Duration.String without zero units: "1h", not "1h0m0s".
func formatPeriod(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

func (r Rate) apply(c *config) { c.rate = &r }

// Algorithm selects how a rule counts requests. See the algorithm package
// and docs/algorithms.md for the trade-offs.
type Algorithm = algorithm.ID

// The available algorithms. GCRA is the default.
const (
	FixedWindow   = algorithm.FixedWindow
	SlidingLog    = algorithm.SlidingLog
	SlidingWindow = algorithm.SlidingWindow
	TokenBucket   = algorithm.TokenBucket
	LeakyBucket   = algorithm.LeakyBucket
	GCRA          = algorithm.GCRA
)

// Rule is one limit applied to every key of a limiter.
type Rule struct {
	// Name identifies the rule in storage keys and in the RateLimit headers.
	// It defaults to "<algorithm>_<limit>_<period>", e.g. "gcra_10_1s".
	// Limiters sharing a store must use distinct names unless they are meant
	// to share counters.
	Name string
	// Rate is the sustained rate.
	Rate Rate
	// Burst is the bucket capacity for TokenBucket, LeakyBucket and GCRA.
	// It defaults to Rate.Limit and is ignored by windowed algorithms.
	Burst int
	// Algorithm defaults to GCRA.
	Algorithm Algorithm
}

// Capacity is the largest number of events the rule allows at once.
func (r Rule) Capacity() int {
	if r.Algorithm.Windowed() {
		return r.Rate.Limit
	}
	if r.Burst == 0 {
		return r.Rate.Limit
	}
	return r.Burst
}

// Params converts the rule to algorithm parameters.
func (r Rule) Params() algorithm.Params {
	return algorithm.Params{
		Limit:  int64(r.Rate.Limit),
		Period: r.Rate.Period,
		Burst:  int64(r.Capacity()),
	}
}

// normalize fills in defaults and validates the rule.
func (r Rule) normalize() (Rule, error) {
	if r.Algorithm == 0 {
		r.Algorithm = GCRA
	}
	if !r.Algorithm.Valid() {
		return r, fmt.Errorf("ratelimit: unknown algorithm %v", r.Algorithm)
	}
	if r.Rate.Limit <= 0 {
		return r, errors.New("ratelimit: rate limit must be positive")
	}
	if r.Rate.Period < time.Microsecond {
		return r, errors.New("ratelimit: rate period must be at least 1µs")
	}
	if r.Burst < 0 {
		return r, errors.New("ratelimit: burst must not be negative")
	}
	if r.Burst == 0 || r.Algorithm.Windowed() {
		r.Burst = r.Rate.Limit
	}
	if r.Name == "" {
		r.Name = r.Algorithm.String() + "_" + strconv.Itoa(r.Rate.Limit) + "_" + formatPeriod(r.Rate.Period)
	}
	return r, nil
}
