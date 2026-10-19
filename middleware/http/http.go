// Package rlhttp is net/http rate limiting middleware for go-ratelimit.
//
//	lim, _ := ratelimit.New(ratelimit.PerSecond(10), ratelimit.WithStore(memory.New()))
//	handler := rlhttp.New(lim)(mux)
//
// By default requests are keyed by the connection's peer IP, denied requests
// get 429 Too Many Requests with Retry-After, and every response carries the
// IETF RateLimit and RateLimit-Policy headers.
package rlhttp

import (
	"fmt"
	"net/http"
	"time"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
)

// KeyFunc extracts the rate limit key from a request.
type KeyFunc func(r *http.Request) (string, error)

// ErrMissingKey is returned by key functions when the request does not carry
// the expected key. The default error handler answers 400 Bad Request.
var ErrMissingKey = middleware.ErrMissingKey

// KeyByIP keys requests by client IP as resolved by res. A nil resolver uses
// the peer address and ignores forwarding headers.
func KeyByIP(res *middleware.IPResolver) KeyFunc {
	return func(r *http.Request) (string, error) {
		ip, err := res.ClientIP(r.RemoteAddr, r.Header.Values)
		if err != nil {
			return "", err
		}
		return ip.String(), nil
	}
}

// KeyByHeader keys requests by the value of a header, such as an API key.
func KeyByHeader(name string) KeyFunc {
	return func(r *http.Request) (string, error) {
		if v := r.Header.Get(name); v != "" {
			return v, nil
		}
		return "", ErrMissingKey
	}
}

// KeyByContextValue keys requests by a context value set by earlier
// middleware, such as an authenticated user ID. The value is formatted with
// fmt.Sprint.
func KeyByContextValue(ctxKey any) KeyFunc {
	return func(r *http.Request) (string, error) {
		switch v := r.Context().Value(ctxKey).(type) {
		case nil:
			return "", ErrMissingKey
		case string:
			if v == "" {
				return "", ErrMissingKey
			}
			return v, nil
		default:
			return fmt.Sprint(v), nil
		}
	}
}

// KeyByRouteAndIP keys requests by route and client IP, giving each client
// a separate limit per endpoint. route defaults to "METHOD /path"; pass a
// router-aware function to group paths with parameters.
func KeyByRouteAndIP(route func(r *http.Request) string, res *middleware.IPResolver) KeyFunc {
	if route == nil {
		route = func(r *http.Request) string { return r.Method + " " + r.URL.Path }
	}
	ip := KeyByIP(res)
	return func(r *http.Request) (string, error) {
		k, err := ip(r)
		if err != nil {
			return "", err
		}
		return route(r) + "|" + k, nil
	}
}

// FirstOf returns the first key that the functions produce without error,
// for example KeyByHeader("X-API-Key") falling back to KeyByIP.
func FirstOf(fns ...KeyFunc) KeyFunc {
	return func(r *http.Request) (string, error) {
		err := ErrMissingKey
		for _, fn := range fns {
			var k string
			if k, err = fn(r); err == nil {
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
	skip    func(r *http.Request) bool
	deny    func(w http.ResponseWriter, r *http.Request, res ratelimit.Result)
	onError func(w http.ResponseWriter, r *http.Request, err error)
	now     func() time.Time
}

// WithKeyFunc sets how requests are keyed. The default is KeyByIP(nil).
func WithKeyFunc(fn KeyFunc) Option { return func(c *config) { c.key = fn } }

// WithHeaders selects the rate limit headers. The default is
// middleware.HeadersIETF.
func WithHeaders(style middleware.HeaderStyle) Option { return func(c *config) { c.headers = style } }

// WithJSONBody makes the default deny handler answer with a JSON body (see
// middleware.DenyBody) instead of plain text.
func WithJSONBody() Option { return func(c *config) { c.json = true } }

// WithSkip bypasses the limiter for requests where skip returns true, such
// as health checks.
func WithSkip(skip func(r *http.Request) bool) Option { return func(c *config) { c.skip = skip } }

// WithDenyHandler replaces the response for denied requests. Rate limit
// headers and Retry-After are already set when it is called.
func WithDenyHandler(fn func(w http.ResponseWriter, r *http.Request, res ratelimit.Result)) Option {
	return func(c *config) { c.deny = fn }
}

// WithErrorHandler replaces the response when the key cannot be extracted or
// the limiter fails closed. The default answers 400 for ErrMissingKey,
// 503 for ratelimit.ErrStore and 500 otherwise.
func WithErrorHandler(fn func(w http.ResponseWriter, r *http.Request, err error)) Option {
	return func(c *config) { c.onError = fn }
}

// WithNow sets the time source for the absolute X-RateLimit-Reset header.
func WithNow(now func() time.Time) Option { return func(c *config) { c.now = now } }

// New returns middleware that rate limits requests with lim.
func New(lim ratelimit.Limiter, opts ...Option) func(http.Handler) http.Handler {
	c := config{key: KeyByIP(nil), headers: middleware.DefaultHeaders, onError: defaultError, now: time.Now}
	for _, o := range opts {
		o(&c)
	}
	if c.deny == nil {
		c.deny = denyText
		if c.json {
			c.deny = denyJSON
		}
	}
	hw := middleware.NewHeaderWriter(lim, c.headers)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.skip != nil && c.skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			key, err := c.key(r)
			if err != nil {
				c.onError(w, r, err)
				return
			}
			res, err := lim.Allow(r.Context(), key)
			if err != nil {
				c.onError(w, r, err)
				return
			}
			hw.Write(w.Header().Set, res, c.now())
			if !res.Allowed {
				c.deny(w, r, res)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func denyText(w http.ResponseWriter, _ *http.Request, _ ratelimit.Result) {
	http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
}

func denyJSON(w http.ResponseWriter, _ *http.Request, res ratelimit.Result) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	w.Write(middleware.DenyBody(res))
}

func defaultError(w http.ResponseWriter, _ *http.Request, err error) {
	status := middleware.ErrorStatus(err)
	http.Error(w, http.StatusText(status), status)
}
