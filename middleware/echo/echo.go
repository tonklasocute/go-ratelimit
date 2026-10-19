// Package rlecho adapts go-ratelimit to Echo v5.
//
//	e := echo.New()
//	e.Use(rlecho.New(lim, rlecho.WithKeyFunc(rlecho.KeyByRouteAndIP(nil))))
//
// Behavior matches rlhttp: 429 with Retry-After on denial, RateLimit headers
// on every response, 400 for a missing key and 503 when the limiter fails
// closed.
package rlecho

import (
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
)

// KeyFunc extracts the rate limit key from a request.
type KeyFunc func(c *echo.Context) (string, error)

// KeyByIP keys requests by client IP as resolved by res; nil uses the peer
// address. It does not use c.RealIP, whose proxy trust is configured on the
// Echo instance.
func KeyByIP(res *middleware.IPResolver) KeyFunc {
	return func(c *echo.Context) (string, error) {
		r := c.Request()
		ip, err := res.ClientIP(r.RemoteAddr, r.Header.Values)
		if err != nil {
			return "", err
		}
		return ip.String(), nil
	}
}

// KeyByHeader keys requests by a header value, such as an API key.
func KeyByHeader(name string) KeyFunc {
	return func(c *echo.Context) (string, error) {
		if v := c.Request().Header.Get(name); v != "" {
			return v, nil
		}
		return "", middleware.ErrMissingKey
	}
}

// KeyByContextValue keys requests by a value stored with c.Set by earlier
// middleware, such as an authenticated user ID.
func KeyByContextValue(key string) KeyFunc {
	return func(c *echo.Context) (string, error) {
		v := c.Get(key)
		if v == nil || v == "" {
			return "", middleware.ErrMissingKey
		}
		return fmt.Sprint(v), nil
	}
}

// KeyByRouteAndIP keys requests by method, Echo route pattern (c.Path) and
// client IP.
func KeyByRouteAndIP(res *middleware.IPResolver) KeyFunc {
	ip := KeyByIP(res)
	return func(c *echo.Context) (string, error) {
		k, err := ip(c)
		if err != nil {
			return "", err
		}
		route := c.Path()
		if route == "" {
			route = c.Request().URL.Path
		}
		return c.Request().Method + " " + route + "|" + k, nil
	}
}

// FirstOf returns the first key produced without error.
func FirstOf(fns ...KeyFunc) KeyFunc {
	return func(c *echo.Context) (string, error) {
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
	skip    func(c *echo.Context) bool
	deny    func(c *echo.Context, res ratelimit.Result) error
	onError func(c *echo.Context, err error) error
	now     func() time.Time
}

// WithKeyFunc sets how requests are keyed. The default is KeyByIP(nil).
func WithKeyFunc(fn KeyFunc) Option { return func(c *config) { c.key = fn } }

// WithHeaders selects the rate limit headers (default middleware.HeadersIETF).
func WithHeaders(style middleware.HeaderStyle) Option { return func(c *config) { c.headers = style } }

// WithJSONBody answers denials with a JSON body instead of plain text.
func WithJSONBody() Option { return func(c *config) { c.json = true } }

// WithSkip bypasses the limiter when skip returns true.
func WithSkip(skip func(c *echo.Context) bool) Option { return func(c *config) { c.skip = skip } }

// WithDenyHandler replaces the denial response. Headers are already set.
func WithDenyHandler(fn func(c *echo.Context, res ratelimit.Result) error) Option {
	return func(c *config) { c.deny = fn }
}

// WithErrorHandler replaces the response to key and limiter errors.
func WithErrorHandler(fn func(c *echo.Context, err error) error) Option {
	return func(c *config) { c.onError = fn }
}

// WithNow sets the time source for the absolute X-RateLimit-Reset header.
func WithNow(now func() time.Time) Option { return func(c *config) { c.now = now } }

// New returns Echo middleware rate limiting requests with lim.
func New(lim ratelimit.Limiter, opts ...Option) echo.MiddlewareFunc {
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
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if cfg.skip != nil && cfg.skip(c) {
				return next(c)
			}
			key, err := cfg.key(c)
			if err != nil {
				return cfg.onError(c, err)
			}
			res, err := lim.Allow(c.Request().Context(), key)
			if err != nil {
				return cfg.onError(c, err)
			}
			hw.Write(c.Response().Header().Set, res, cfg.now())
			if !res.Allowed {
				return cfg.deny(c, res)
			}
			return next(c)
		}
	}
}

func denyText(c *echo.Context, _ ratelimit.Result) error {
	return c.String(http.StatusTooManyRequests, http.StatusText(http.StatusTooManyRequests))
}

func denyJSON(c *echo.Context, res ratelimit.Result) error {
	return c.Blob(http.StatusTooManyRequests, "application/json", middleware.DenyBody(res))
}

func defaultError(c *echo.Context, err error) error {
	status := middleware.ErrorStatus(err)
	return c.String(status, http.StatusText(status))
}
