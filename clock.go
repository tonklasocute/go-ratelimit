package ratelimit

import "time"

// Clock tells the time. Inject a fake clock (see the ratelimittest package)
// to make tests deterministic.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SystemClock returns a Clock backed by time.Now.
func SystemClock() Clock { return systemClock{} }
