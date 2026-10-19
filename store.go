package ratelimit

import (
	"context"
	"time"

	"github.com/tonklasocute/go-ratelimit/algorithm"
)

// Store keeps limiter state. Implementations must be safe for concurrent use.
//
// Take applies every rule to key atomically: if any rule denies the request,
// no rule consumes anything. It returns the most restrictive result (see
// MostRestrictive). now is the limiter's clock reading; stores with their own
// time source, such as Redis server time, may ignore it.
type Store interface {
	Take(ctx context.Context, key string, rules []Rule, n int, now time.Time) (Result, error)
	Reset(ctx context.Context, key string, rules []Rule) error
}

// NewResult converts an algorithm decision for rule into a Result.
func NewResult(rule Rule, d algorithm.Decision) Result {
	return Result{
		Allowed:    d.Allowed,
		Limit:      int(d.Limit),
		Remaining:  int(d.Remaining),
		ResetAfter: d.ResetAfter,
		RetryAfter: d.RetryAfter,
		Policy:     rule.Name,
	}
}

// MostRestrictive returns whichever of a and b should be reported to the
// client: a denial over an allowance, the longer RetryAfter among denials,
// and the fewer Remaining (then the longer ResetAfter) among allowances.
// The zero Result acts as the identity, so it can seed a fold.
func MostRestrictive(a, b Result) Result {
	switch {
	case a.Policy == "":
		return b
	case b.Policy == "":
		return a
	case a.Allowed != b.Allowed:
		if !a.Allowed {
			return a
		}
		return b
	case !a.Allowed:
		if b.RetryAfter > a.RetryAfter {
			return b
		}
		return a
	case b.Remaining < a.Remaining, b.Remaining == a.Remaining && b.ResetAfter > a.ResetAfter:
		return b
	default:
		return a
	}
}
