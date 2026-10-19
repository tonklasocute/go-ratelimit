// Package rlchi adapts go-ratelimit to the chi router.
//
// chi uses standard net/http middleware, so the middleware itself is
// rlhttp.New; this package adds keys based on chi's route patterns and URL
// parameters:
//
//	r := chi.NewRouter()
//	r.Use(rlchi.New(lim, rlhttp.WithKeyFunc(rlchi.KeyByRouteAndIP(nil))))
package rlchi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
	rlhttp "github.com/tonklasocute/go-ratelimit/middleware/http"
)

// New returns chi middleware rate limiting requests with lim. It accepts the
// options of rlhttp.
func New(lim ratelimit.Limiter, opts ...rlhttp.Option) func(http.Handler) http.Handler {
	return rlhttp.New(lim, opts...)
}

// RoutePattern returns the chi route pattern matching r, such as
// "/users/{id}". Middleware registered with Router.Use runs before routing
// has finished, so the pattern is looked up in the routing tree. It falls
// back to the request path outside a chi router.
func RoutePattern(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return r.URL.Path
	}
	if p := rctx.RoutePattern(); p != "" {
		return p
	}
	if rctx.Routes != nil {
		if p := rctx.Routes.Find(chi.NewRouteContext(), r.Method, r.URL.Path); p != "" {
			return p
		}
	}
	return r.URL.Path
}

// KeyByRouteAndIP keys requests by method, chi route pattern and client IP,
// so "/users/1" and "/users/2" share the limit of "GET /users/{id}".
func KeyByRouteAndIP(res *middleware.IPResolver) rlhttp.KeyFunc {
	return rlhttp.KeyByRouteAndIP(func(r *http.Request) string { return r.Method + " " + RoutePattern(r) }, res)
}

// KeyByURLParam keys requests by a URL parameter, such as a tenant ID in
// "/tenants/{tenant}/...". It must run after routing, e.g. via Router.With
// or inside a route group.
func KeyByURLParam(name string) rlhttp.KeyFunc {
	return func(r *http.Request) (string, error) {
		if v := chi.URLParam(r, name); v != "" {
			return v, nil
		}
		return "", middleware.ErrMissingKey
	}
}
