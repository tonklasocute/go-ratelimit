// Package ratelimittest provides helpers for testing code that uses
// go-ratelimit: a manually advanced clock and a conformance suite for Store
// implementations.
package ratelimittest

import (
	"sync"
	"time"
)

// Epoch is the default start time of a Clock. It is a round number so that
// window boundaries in tests are easy to reason about.
var Epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Clock is a fake ratelimit.Clock that only moves when told to.
// It is safe for concurrent use.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock set to Epoch.
func NewClock() *Clock { return &Clock{now: Epoch} }

// NewClockAt returns a clock set to t.
func NewClockAt(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Set moves the clock to t, which may be in the past.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}
