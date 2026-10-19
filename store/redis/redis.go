// Package redisstore is a Redis-backed ratelimit.Store for limits shared by
// several application instances.
//
// Every check is one Lua script call (EVALSHA, falling back to EVAL when
// the script is not cached yet). Redis executes a script atomically, so
// concurrent instances cannot interleave between reading and writing a
// limit, and all rules of a check are applied all-or-nothing.
//
// By default the script reads the current time from Redis (TIME) rather
// than from the application, so clock skew between instances cannot loosen
// or tighten limits.
//
// Keys look like "ratelimit:{<key>}:<rule>". The {hash tag} puts all rules
// of a key in the same Redis Cluster slot, which a multi-key script requires.
package redisstore

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	ratelimit "github.com/tonklasocute/go-ratelimit"
)

//go:embed ratelimit.lua
var scriptSource string

var script = redis.NewScript(scriptSource)

// Client is the subset of the go-redis API the store needs. *redis.Client,
// *redis.ClusterClient, *redis.Ring and redis.UniversalClient implement it.
type Client interface {
	redis.Scripter
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// Store is a Redis ratelimit.Store.
type Store struct {
	client     Client
	prefix     string
	clientTime bool
}

var _ ratelimit.Store = (*Store)(nil)

// Option configures a Store.
type Option func(*Store)

// WithPrefix sets the prefix of every key. The default is "ratelimit:".
// The prefix must not contain '{' or '}', which would change the hash tag.
func WithPrefix(prefix string) Option { return func(s *Store) { s.prefix = prefix } }

// WithClientTime uses the limiter's clock instead of the Redis server time.
// It makes tests with a fake clock deterministic; in production prefer
// server time, which is immune to clock skew between instances.
func WithClientTime() Option { return func(s *Store) { s.clientTime = true } }

// New creates a Store using client.
func New(client Client, opts ...Option) *Store {
	s := &Store{client: client, prefix: "ratelimit:"}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Take applies rules to key atomically. See ratelimit.Store. Redis errors
// are returned as is, for the limiter's failure policy to handle.
func (s *Store) Take(ctx context.Context, key string, rules []ratelimit.Rule, n int, now time.Time) (ratelimit.Result, error) {
	keys := s.keys(key, rules)
	args := make([]any, 0, 2+4*len(rules))
	args = append(args, n, "")
	if s.clientTime {
		args[1] = now.UnixMicro()
	}
	for _, r := range rules {
		args = append(args, uint8(r.Algorithm), r.Rate.Limit, r.Rate.Period.Microseconds(), r.Capacity())
	}
	reply, err := s.eval(ctx, keys, args)
	if err != nil {
		return ratelimit.Result{}, err
	}
	vals, ok := reply.([]any)
	if !ok || len(vals) != 5*len(rules) {
		return ratelimit.Result{}, fmt.Errorf("redisstore: unexpected script reply %v", reply)
	}
	var res ratelimit.Result
	for i, r := range rules {
		v := vals[5*i : 5*i+5]
		res = ratelimit.MostRestrictive(res, ratelimit.Result{
			Allowed:    toInt(v[0]) == 1,
			Limit:      int(toInt(v[1])),
			Remaining:  int(toInt(v[2])),
			ResetAfter: time.Duration(toInt(v[3])) * time.Microsecond,
			RetryAfter: time.Duration(toInt(v[4])) * time.Microsecond,
			Policy:     r.Name,
		})
	}
	return res, nil
}

// Reset deletes the state of rules for key.
func (s *Store) Reset(ctx context.Context, key string, rules []ratelimit.Rule) error {
	if len(rules) == 0 {
		return nil
	}
	return s.client.Del(ctx, s.keys(key, rules)...).Err()
}

// Key returns the Redis key holding the state of rule for key.
func (s *Store) Key(key, rule string) string {
	return s.prefix + hashTag(key) + ":" + rule
}

func (s *Store) keys(key string, rules []ratelimit.Rule) []string {
	keys := make([]string, len(rules))
	for i, r := range rules {
		keys[i] = s.Key(key, r.Name)
	}
	return keys
}

// eval runs the script by SHA and loads it with EVAL if Redis has not cached
// it yet (after a restart, failover or SCRIPT FLUSH).
func (s *Store) eval(ctx context.Context, keys []string, args []any) (any, error) {
	reply, err := s.client.EvalSha(ctx, script.Hash(), keys, args...).Result()
	if err != nil && redis.HasErrorPrefix(err, "NOSCRIPT") {
		reply, err = s.client.Eval(ctx, scriptSource, keys, args...).Result()
	}
	return reply, err
}

// hashTagEscaper escapes the characters that would end the hash tag early.
// The escaping is injective, so distinct keys never share a Redis key.
var hashTagEscaper = strings.NewReplacer(`\`, `\\`, `}`, `\]`)

// hashTag wraps key in braces. An empty key gets a placeholder because Redis
// ignores an empty tag "{}" and would scatter the rules across slots.
func hashTag(key string) string {
	if key == "" {
		return `{\0}`
	}
	return "{" + hashTagEscaper.Replace(key) + "}"
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case string:
		i, _ := strconv.ParseInt(x, 10, 64)
		return i
	default:
		return 0
	}
}
