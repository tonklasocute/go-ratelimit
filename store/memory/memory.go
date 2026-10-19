// Package memory provides an in-process ratelimit.Store.
//
// Keys are spread over independently locked shards to reduce lock
// contention. Each key expires once all of its rules are back to their fresh
// state; a background goroutine deletes expired keys until Close is called.
// An optional key cap evicts the least recently used key of a shard when it
// is full. Evicting a key resets its limits, so size the cap generously.
//
// State lives in the process: with several instances of an application,
// each enforces its own limit. Use the Redis store to share limits.
package memory

import (
	"context"
	"sync"
	"time"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/algorithm"
)

// Store is an in-memory ratelimit.Store. Create it with New and stop its
// cleanup goroutine with Close.
type Store struct {
	shards   []shard
	mask     uint64
	perShard int // max keys per shard, 0 = unlimited
	clock    ratelimit.Clock

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

var _ ratelimit.Store = (*Store)(nil)

type shard struct {
	mu         sync.Mutex
	m          map[string]*entry
	head, tail *entry // LRU list, most recently used first
	_          [64]byte
}

type entry struct {
	key        string
	expires    int64 // Unix ns after which every rule is fresh again
	prev, next *entry
	rules      []ruleState
	inline     [1]ruleState // backing array for the common single-rule case
}

type ruleState struct {
	name  string
	state algorithm.State
}

// Option configures a Store.
type Option func(*options)

type options struct {
	shards  int
	maxKeys int
	cleanup time.Duration
	clock   ratelimit.Clock
}

// WithShards sets the number of shards, rounded up to a power of two.
// The default is 64.
func WithShards(n int) Option { return func(o *options) { o.shards = n } }

// WithMaxKeys caps the number of keys. The cap is split evenly across shards,
// so it is enforced approximately. The default, 0, means no cap.
func WithMaxKeys(n int) Option { return func(o *options) { o.maxKeys = n } }

// WithCleanupInterval sets how often expired keys are deleted. The default is
// one minute; 0 disables the cleanup goroutine.
func WithCleanupInterval(d time.Duration) Option { return func(o *options) { o.cleanup = d } }

// WithClock sets the clock used by the cleanup goroutine. Pass the same clock
// as the limiter when using a fake clock.
func WithClock(c ratelimit.Clock) Option { return func(o *options) { o.clock = c } }

// New creates a Store.
func New(opts ...Option) *Store {
	o := options{shards: 64, cleanup: time.Minute, clock: ratelimit.SystemClock()}
	for _, opt := range opts {
		opt(&o)
	}
	n := 1
	for n < o.shards {
		n <<= 1
	}
	s := &Store{clock: o.clock, stop: make(chan struct{}), done: make(chan struct{})}
	if o.maxKeys > 0 {
		for n > 1 && n > o.maxKeys {
			n >>= 1
		}
		s.perShard = (o.maxKeys + n - 1) / n
	}
	s.shards = make([]shard, n)
	s.mask = uint64(n - 1)
	for i := range s.shards {
		s.shards[i].m = make(map[string]*entry)
	}
	if o.cleanup > 0 {
		go s.janitor(o.cleanup)
	} else {
		close(s.done)
	}
	return s
}

// Take applies rules to key atomically. See ratelimit.Store.
func (s *Store) Take(ctx context.Context, key string, rules []ratelimit.Rule, n int, now time.Time) (ratelimit.Result, error) {
	if err := ctx.Err(); err != nil {
		return ratelimit.Result{}, err
	}
	ts := now.UnixNano()
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e := sh.get(key, s.perShard)
	for i := range rules {
		e.ensure(rules[i].Name)
	}
	var res ratelimit.Result
	var ttl time.Duration
	for i := range rules {
		r := &rules[i]
		d := algorithm.Peek(r.Algorithm, e.state(r.Name), r.Params(), ts, int64(n))
		res = ratelimit.MostRestrictive(res, ratelimit.NewResult(*r, d))
		ttl = max(ttl, d.ResetAfter)
	}
	if res.Allowed && n > 0 {
		for i := range rules {
			r := &rules[i]
			algorithm.Commit(r.Algorithm, e.state(r.Name), r.Params(), ts, int64(n))
		}
	}
	e.expires = max(e.expires, ts+int64(ttl))
	return res, nil
}

// Reset deletes the state of rules for key.
func (s *Store) Reset(ctx context.Context, key string, rules []ratelimit.Rule) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.m[key]
	if !ok {
		return nil
	}
	kept := e.rules[:0]
	for _, rs := range e.rules {
		if !hasRule(rules, rs.name) {
			kept = append(kept, rs)
		}
	}
	clear(e.rules[len(kept):])
	e.rules = kept
	if len(kept) == 0 {
		sh.remove(e)
	}
	return nil
}

// Len returns the number of keys currently stored.
func (s *Store) Len() int {
	total := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		total += len(sh.m)
		sh.mu.Unlock()
	}
	return total
}

// DeleteExpired deletes keys whose rules are all fresh again and returns how
// many were deleted. The cleanup goroutine calls it periodically.
func (s *Store) DeleteExpired() int {
	now := s.clock.Now().UnixNano()
	deleted := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		for _, e := range sh.m {
			if e.expires <= now {
				sh.remove(e)
				deleted++
			}
		}
		sh.mu.Unlock()
	}
	return deleted
}

// Close stops the cleanup goroutine. The store remains usable. Close is
// idempotent and always returns nil.
func (s *Store) Close() error {
	s.closeOnce.Do(func() { close(s.stop) })
	<-s.done
	return nil
}

func (s *Store) janitor(interval time.Duration) {
	defer close(s.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.DeleteExpired()
		}
	}
}

// shard picks a shard by FNV-1a hash of the key.
func (s *Store) shard(key string) *shard {
	h := uint64(14695981039346656037)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return &s.shards[h&s.mask]
}

// get returns the entry for key, creating it and evicting the least recently
// used entry if the shard is full. The entry becomes the most recently used.
func (sh *shard) get(key string, limit int) *entry {
	if e, ok := sh.m[key]; ok {
		sh.unlink(e)
		sh.pushFront(e)
		return e
	}
	if limit > 0 && len(sh.m) >= limit && sh.tail != nil {
		sh.remove(sh.tail)
	}
	e := &entry{key: key}
	e.rules = e.inline[:0]
	sh.m[key] = e
	sh.pushFront(e)
	return e
}

func (sh *shard) remove(e *entry) {
	sh.unlink(e)
	delete(sh.m, e.key)
}

func (sh *shard) pushFront(e *entry) {
	e.prev, e.next = nil, sh.head
	if sh.head != nil {
		sh.head.prev = e
	}
	sh.head = e
	if sh.tail == nil {
		sh.tail = e
	}
}

func (sh *shard) unlink(e *entry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		sh.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		sh.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (e *entry) ensure(name string) {
	if e.state(name) == nil {
		e.rules = append(e.rules, ruleState{name: name})
	}
}

func (e *entry) state(name string) *algorithm.State {
	for i := range e.rules {
		if e.rules[i].name == name {
			return &e.rules[i].state
		}
	}
	return nil
}

func hasRule(rules []ratelimit.Rule, name string) bool {
	for i := range rules {
		if rules[i].Name == name {
			return true
		}
	}
	return false
}
