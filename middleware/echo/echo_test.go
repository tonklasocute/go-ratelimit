package rlecho_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
	rlecho "github.com/tonklasocute/go-ratelimit/middleware/echo"
	"github.com/tonklasocute/go-ratelimit/ratelimittest"
	"github.com/tonklasocute/go-ratelimit/store/memory"
)

func newLimiter(t *testing.T) ratelimit.Limiter {
	t.Helper()
	lim, err := ratelimit.New(ratelimit.PerMinute(1),
		ratelimit.WithStore(memory.New(memory.WithCleanupInterval(0))),
		ratelimit.WithClock(ratelimittest.NewClock()))
	if err != nil {
		t.Fatal(err)
	}
	return lim
}

func do(h http.Handler, path string, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "192.0.2.1:1234"
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func server(mw ...echo.MiddlewareFunc) *echo.Echo {
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if u := c.Request().Header.Get("X-User"); u != "" {
				c.Set("user_id", u)
			}
			return next(c)
		}
	})
	e.Use(mw...)
	ok := func(c *echo.Context) error { return c.String(http.StatusOK, "ok") }
	e.GET("/users/:id", ok)
	e.GET("/healthz", ok)
	return e
}

func TestAllowsThenDenies(t *testing.T) {
	e := server(rlecho.New(newLimiter(t)))
	w := do(e, "/users/1", nil)
	if w.Code != http.StatusOK || w.Header().Get("RateLimit") != `"gcra_1_1m";r=0;t=60` {
		t.Fatalf("first: %d %v", w.Code, w.Header())
	}
	w = do(e, "/users/1", nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" || w.Body.String() != "Too Many Requests" {
		t.Fatalf("second: %d %q %v", w.Code, w.Body, w.Header())
	}
}

func TestPerUserJSON(t *testing.T) {
	e := server(rlecho.New(newLimiter(t), rlecho.WithKeyFunc(rlecho.KeyByContextValue("user_id")), rlecho.WithJSONBody()))
	alice := func(r *http.Request) { r.Header.Set("X-User", "alice") }
	do(e, "/users/1", alice)
	w := do(e, "/users/1", alice)
	if want := `{"error":"rate limit exceeded","limit":1,"remaining":0,"retry_after":60}`; w.Code != http.StatusTooManyRequests || w.Body.String() != want {
		t.Fatalf("alice second: %d %s", w.Code, w.Body)
	}
	if w := do(e, "/users/1", func(r *http.Request) { r.Header.Set("X-User", "bob") }); w.Code != http.StatusOK {
		t.Fatalf("bob: %d", w.Code)
	}
	if w := do(e, "/users/1", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("anonymous: %d", w.Code)
	}
}

func TestSkipDenyAndHeaders(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e := server(rlecho.New(newLimiter(t),
		rlecho.WithSkip(func(c *echo.Context) bool { return c.Path() == "/healthz" }),
		rlecho.WithHeaders(middleware.HeadersIETFLegacy|middleware.HeadersXRateLimit),
		rlecho.WithNow(func() time.Time { return now }),
		rlecho.WithDenyHandler(func(c *echo.Context, res ratelimit.Result) error {
			return c.NoContent(http.StatusServiceUnavailable)
		}),
	))
	for i := 0; i < 3; i++ {
		if w := do(e, "/healthz", nil); w.Code != http.StatusOK {
			t.Fatalf("health %d: %d", i, w.Code)
		}
	}
	w := do(e, "/users/1", nil)
	if w.Header().Get("X-RateLimit-Reset") != "1700000060" || w.Header().Get("RateLimit-Limit") != "1" {
		t.Fatalf("headers %v", w.Header())
	}
	if w := do(e, "/users/1", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("custom deny: %d", w.Code)
	}
}

func TestKeyFuncs(t *testing.T) {
	trusted, _ := middleware.NewIPResolver(middleware.TrustProxies("10.0.0.0/8"))
	tests := []struct {
		name    string
		fn      rlecho.KeyFunc
		mod     func(*http.Request)
		want    string
		wantErr error
	}{
		{"ip", rlecho.KeyByIP(nil), nil, "192.0.2.1", nil},
		{"ip behind proxy", rlecho.KeyByIP(trusted), func(r *http.Request) {
			r.RemoteAddr = "10.0.0.1:1"
			r.Header.Set("X-Forwarded-For", "198.51.100.7")
		}, "198.51.100.7", nil},
		{"bad ip", rlecho.KeyByIP(nil), func(r *http.Request) { r.RemoteAddr = "@" }, "", middleware.ErrNoClientIP},
		{"header", rlecho.KeyByHeader("X-API-Key"), func(r *http.Request) { r.Header.Set("X-API-Key", "k1") }, "k1", nil},
		{"header missing", rlecho.KeyByHeader("X-API-Key"), nil, "", middleware.ErrMissingKey},
		{"route and ip", rlecho.KeyByRouteAndIP(nil), nil, "GET /users/:id|192.0.2.1", nil},
		{"route and bad ip", rlecho.KeyByRouteAndIP(nil), func(r *http.Request) { r.RemoteAddr = "@" }, "", middleware.ErrNoClientIP},
		{"first of", rlecho.FirstOf(rlecho.KeyByHeader("X-API-Key"), rlecho.KeyByIP(nil)), nil, "192.0.2.1", nil},
		{"first of none", rlecho.FirstOf(), nil, "", middleware.ErrMissingKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			var err error
			e := server(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c *echo.Context) error {
					got, err = tt.fn(c)
					return next(c)
				}
			})
			do(e, "/users/7", tt.mod)
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

type failingStore struct{}

func (failingStore) Take(context.Context, string, []ratelimit.Rule, int, time.Time) (ratelimit.Result, error) {
	return ratelimit.Result{}, errors.New("down")
}
func (failingStore) Reset(context.Context, string, []ratelimit.Rule) error { return nil }

func TestErrors(t *testing.T) {
	lim, _ := ratelimit.New(ratelimit.PerSecond(1), ratelimit.WithStore(failingStore{}), ratelimit.WithFailurePolicy(ratelimit.FailClosed))
	if w := do(server(rlecho.New(lim)), "/users/1", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("fail closed: %d", w.Code)
	}
	e := server(rlecho.New(lim, rlecho.WithErrorHandler(func(c *echo.Context, err error) error {
		return c.NoContent(http.StatusTeapot)
	})))
	if w := do(e, "/users/1", nil); w.Code != http.StatusTeapot {
		t.Fatalf("custom error: %d", w.Code)
	}
}
