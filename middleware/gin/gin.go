// Package rlgin adapts go-ratelimit to the Gin web framework.
//
//	r := gin.New()
//	r.Use(rlgin.New(lim, rlgin.WithKeyFunc(rlgin.KeyByContextValue("user_id"))))
//
// Behavior matches rlhttp: 429 with Retry-After on denial, RateLimit headers
// on every response, 400 for a missing key and 503 when the limiter fails
// closed.
package rlgin

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
)

// KeyFunc extracts the rate limit key from a request.
type KeyFunc func(c *gin.Context) (string, error)

// KeyByIP keys requests by client IP as resolved by res; nil uses the peer
// address. It deliberately does not use c.ClientIP, whose proxy trust is
// configured separately on the engine.
func KeyByIP(res *middleware.IPResolver) KeyFunc {
	return func(c *gin.Context) (string, error) {
		ip, err := res.ClientIP(c.Request.RemoteAddr, c.Request.Header.Values)
		if err != nil {
			return "", err
		}
		return ip.String(), nil
	}
}

// KeyByHeader keys requests by a header value, such as an API key.
func KeyByHeader(name string) KeyFunc {
	return func(c *gin.Context) (string, error) {
		if v := c.GetHeader(name); v != "" {
			return v, nil
		}
		return "", middleware.ErrMissingKey
	}
}

// KeyByContextValue keys requests by a value stored with c.Set by earlier
// middleware, such as an authenticated user ID.
func KeyByContextValue(key string) KeyFunc {
	return func(c *gin.Context) (string, error) {
		v, ok := c.Get(key)
		if !ok || v == nil || v == "" {
			return "", middleware.ErrMissingKey
		}
		return fmt.Sprint(v), nil
	}
}

// KeyByRouteAndIP keys requests by method, Gin route pattern (c.FullPath)
// and client IP.
func KeyByRouteAndIP(res *middleware.IPResolver) KeyFunc {
	ip := KeyByIP(res)
	return func(c *gin.Context) (string, error) {
		k, err := ip(c)
		if err != nil {
			return "", err
		}
		route := c.FullPath()
		if route == "" {
			route = c.Request.URL.Path
		}
		return c.Request.Method + " " + route + "|" + k, nil
	}
}

// FirstOf returns the first key produced without error.
func FirstOf(fns ...KeyFunc) KeyFunc {
	return func(c *gin.Context) (string, error) {
		err := middleware.ErrMissingKey
		for _, fn := range fns {
			var k string
			if k, err = fn(c); err == nil {
				return k, nil
			}
		}
		return "", err
	}
}

// Option configures the middleware.
type Option func(*config)

type config struct {
	key     KeyFunc
	headers middleware.HeaderStyle
	json    bool
	skip    func(c *gin.Context) bool
	deny    func(c *gin.Context, res ratelimit.Result)
	onError func(c *gin.Context, err error)
	now     func() time.Time
}

// WithKeyFunc sets how requests are keyed. The default is KeyByIP(nil).
func WithKeyFunc(fn KeyFunc) Option { return func(c *config) { c.key = fn } }

// WithHeaders selects the rate limit headers (default middleware.HeadersIETF).
func WithHeaders(style middleware.HeaderStyle) Option { return func(c *config) { c.headers = style } }

// WithJSONBody answers denials with a JSON body instead of plain text.
func WithJSONBody() Option { return func(c *config) { c.json = true } }

// WithSkip bypasses the limiter when skip returns true.
func WithSkip(skip func(c *gin.Context) bool) Option { return func(c *config) { c.skip = skip } }

// WithDenyHandler replaces the denial response. Headers are already set; the
// handler must abort the context.
func WithDenyHandler(fn func(c *gin.Context, res ratelimit.Result)) Option {
	return func(c *config) { c.deny = fn }
}

// WithErrorHandler replaces the response to key and limiter errors. The
// handler must abort the context.
func WithErrorHandler(fn func(c *gin.Context, err error)) Option {
	return func(c *config) { c.onError = fn }
}

// WithNow sets the time source for the absolute X-RateLimit-Reset header.
func WithNow(now func() time.Time) Option { return func(c *config) { c.now = now } }

// New returns Gin middleware rate limiting requests with lim.
func New(lim ratelimit.Limiter, opts ...Option) gin.HandlerFunc {
	cfg := config{key: KeyByIP(nil), headers: middleware.DefaultHeaders, onError: defaultError, now: time.Now}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.deny == nil {
		cfg.deny = denyText
		if cfg.json {
			cfg.deny = denyJSON
		}
	}
	hw := middleware.NewHeaderWriter(lim, cfg.headers)
	return func(c *gin.Context) {
		if cfg.skip != nil && cfg.skip(c) {
			c.Next()
			return
		}
		key, err := cfg.key(c)
		if err != nil {
			cfg.onError(c, err)
			return
		}
		res, err := lim.Allow(c.Request.Context(), key)
		if err != nil {
			cfg.onError(c, err)
			return
		}
		hw.Write(c.Header, res, cfg.now())
		if !res.Allowed {
			cfg.deny(c, res)
			return
		}
		c.Next()
	}
}

func denyText(c *gin.Context, _ ratelimit.Result) {
	c.String(http.StatusTooManyRequests, http.StatusText(http.StatusTooManyRequests))
	c.Abort()
}

func denyJSON(c *gin.Context, res ratelimit.Result) {
	c.Data(http.StatusTooManyRequests, "application/json", middleware.DenyBody(res))
	c.Abort()
}

func defaultError(c *gin.Context, err error) {
	status := middleware.ErrorStatus(err)
	c.String(status, http.StatusText(status))
	c.Abort()
}
