// Package ratelimit is a rate limiting library with several algorithms
// behind one interface, in-memory and Redis stores, and HTTP/gRPC middleware.
//
// A limiter combines one or more rules, a store and a clock:
//
//	store := memory.New()
//	defer store.Close()
//
//	lim, err := ratelimit.New(
//		ratelimit.PerSecond(10),
//		ratelimit.WithBurst(20),
//		ratelimit.WithStore(store),
//	)
//	if err != nil {
//		// invalid configuration
//	}
//	res, err := lim.Allow(ctx, "user:42")
//	if err == nil && !res.Allowed {
//		// reject, retry after res.RetryAfter
//	}
//
// The algorithms (fixed window, sliding log, sliding window counter, token
// bucket, leaky bucket and GCRA) are implemented in package algorithm; GCRA
// is the default. Rules given together are enforced atomically: a request is
// allowed only if every rule allows it, and a denied request consumes nothing.
//
// The root module has no dependencies outside the standard library. The Redis
// store and the framework adapters are separate modules.
package ratelimit
