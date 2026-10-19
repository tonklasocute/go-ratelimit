// Package rlfiber adapts go-ratelimit to Fiber v3.
//
//	app := fiber.New()
//	app.Use(rlfiber.New(lim, rlfiber.WithKeyFunc(rlfiber.KeyByHeader("X-API-Key"))))
//
// Behavior matches rlhttp: 429 with Retry-After on denial, RateLimit headers
// on every response, 400 for a missing key and 503 when the limiter fails
// closed.
package rlfiber

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
)

// KeyFunc extracts the rate limit key from a request.
type KeyFunc func(c fiber.Ctx) (string, error)

// KeyByIP keys requests by client IP as resolved by res; nil uses the peer
// address. It does not use c.IP, whose proxy trust is configured on the app.
func KeyByIP(res *middleware.IPResolver) KeyFunc {
	return func(c fiber.Ctx) (string, error) {
		ip, err := res.ClientIP(c.RequestCtx().RemoteAddr().String(), headerValues(c))
		if err != nil {
			return "", err
		}
		return ip.String(), nil
	}
}

// headerValues returns every value of a request header, in order.
func headerValues(c fiber.Ctx) func(name string) []string {
	return func(name string) []string {
		raw := c.RequestCtx().Request.Header.PeekAll(name)
		vals := make([]string, len(raw))
		for i, v := range raw {
			vals[i] = string(v)
		}
		return vals
	}
}

// KeyByHeader keys requests by a header value, such as an API key.
func KeyByHeader(name string) KeyFunc {
	return func(c fiber.Ctx) (string, error) {
		if v := c.Get(name); v != "" {
			return v, nil
		}
		return "", middleware.ErrMissingKey
	}
}

// KeyByLocal keys requests by a value stored with c.Locals by earlier
// middleware, such as an authenticated user ID.
func KeyByLocal(key any) KeyFunc {
	return func(c fiber.Ctx) (string, error) {
		v := c.Locals(key)
		if v == nil || v == "" {
			return "", middleware.ErrMissingKey
		}
		return fmt.Sprint(v), nil
	}
}

// KeyByRouteAndIP keys requests by method, Fiber route pattern and client
// IP. Fiber only knows the route pattern inside route handlers: attach the
// middleware to routes (app.Get("/users/:id", mw, handler)). Under app.Use
// the pattern is the mount path, so all routes would share one key.
func KeyByRouteAndIP(res *middleware.IPResolver) KeyFunc {
	ip := KeyByIP(res)
	return func(c fiber.Ctx) (string, error) {
		k, err := ip(c)
		if err != nil {
			return "", err
		}
		return c.Method() + " " + c.Route().Path + "|" + k, nil
	}
}

// FirstOf returns the first key produced without error.
func FirstOf(fns ...KeyFunc) KeyFunc {
	return func(c fiber.Ctx) (string, error) {
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
	skip    func(c fiber.Ctx) bool
	deny    func(c fiber.Ctx, res ratelimit.Result) error
	onError func(c fiber.Ctx, err error) error
	now     func() time.Time
}

// WithKeyFunc sets how requests are keyed. The default is KeyByIP(nil).
func WithKeyFunc(fn KeyFunc) Option { return func(c *config) { c.key = fn } }

// WithHeaders selects the rate limit headers (default middleware.HeadersIETF).
func WithHeaders(style middleware.HeaderStyle) Option { return func(c *config) { c.headers = style } }

// WithJSONBody answers denials with a JSON body instead of plain text.
func WithJSONBody() Option { return func(c *config) { c.json = true } }

// WithSkip bypasses the limiter when skip returns true.
func WithSkip(skip func(c fiber.Ctx) bool) Option { return func(c *config) { c.skip = skip } }

// WithDenyHandler replaces the denial response. Headers are already set.
func WithDenyHandler(fn func(c fiber.Ctx, res ratelimit.Result) error) Option {
	return func(c *config) { c.deny = fn }
}

// WithErrorHandler replaces the response to key and limiter errors.
func WithErrorHandler(fn func(c fiber.Ctx, err error) error) Option {
	return func(c *config) { c.onError = fn }
}

// WithNow sets the time source for the absolute X-RateLimit-Reset header.
func WithNow(now func() time.Time) Option { return func(c *config) { c.now = now } }

// New returns Fiber middleware rate limiting requests with lim.
func New(lim ratelimit.Limiter, opts ...Option) fiber.Handler {
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
	return func(c fiber.Ctx) error {
		if cfg.skip != nil && cfg.skip(c) {
			return c.Next()
		}
		key, err := cfg.key(c)
		if err != nil {
			return cfg.onError(c, err)
		}
		res, err := lim.Allow(c.Context(), key)
		if err != nil {
			return cfg.onError(c, err)
		}
		hw.Write(c.Set, res, cfg.now())
		if !res.Allowed {
			return cfg.deny(c, res)
		}
		return c.Next()
	}
}

func denyText(c fiber.Ctx, _ ratelimit.Result) error {
	return c.Status(http.StatusTooManyRequests).SendString(http.StatusText(http.StatusTooManyRequests))
}

func denyJSON(c fiber.Ctx, res ratelimit.Result) error {
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return c.Status(http.StatusTooManyRequests).Send(middleware.DenyBody(res))
}

func defaultError(c fiber.Ctx, err error) error {
	status := middleware.ErrorStatus(err)
	return c.Status(status).SendString(http.StatusText(status))
}
