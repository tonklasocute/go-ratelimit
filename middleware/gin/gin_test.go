package rlgin_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
	rlgin "github.com/tonklasocute/go-ratelimit/middleware/gin"
	"github.com/tonklasocute/go-ratelimit/ratelimittest"
	"github.com/tonklasocute/go-ratelimit/store/memory"
)

func init() { gin.SetMode(gin.TestMode) }

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

func router(mw ...gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if u := c.GetHeader("X-User"); u != "" {
			c.Set("user_id", u)
		}
	})
	r.Use(mw...)
	r.GET("/users/:id", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return r
}

func TestAllowsThenDenies(t *testing.T) {
	r := router(rlgin.New(newLimiter(t)))
	w := do(r, "/users/1", nil)
	if w.Code != http.StatusOK || w.Header().Get("RateLimit") != `"gcra_1_1m";r=0;t=60` {
		t.Fatalf("first: %d %v", w.Code, w.Header())
	}
	w = do(r, "/users/1", nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" || w.Body.String() != "Too Many Requests" {
		t.Fatalf("second: %d %q %v", w.Code, w.Body, w.Header())
	}
}

func TestPerUserJSON(t *testing.T) {
	r := router(rlgin.New(newLimiter(t), rlgin.WithKeyFunc(rlgin.KeyByContextValue("user_id")), rlgin.WithJSONBody()))
	alice := func(r *http.Request) { r.Header.Set("X-User", "alice") }
	bob := func(r *http.Request) { r.Header.Set("X-User", "bob") }
	do(r, "/users/1", alice)
	w := do(r, "/users/1", alice)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("alice second: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if want := `{"error":"rate limit exceeded","limit":1,"remaining":0,"retry_after":60}`; w.Body.String() != want {
		t.Fatalf("body %s", w.Body)
	}
	if w := do(r, "/users/1", bob); w.Code != http.StatusOK {
		t.Fatalf("bob: %d", w.Code)
	}
	if w := do(r, "/users/1", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("anonymous: %d", w.Code)
	}
}

func TestSkipDenyAndHeaders(t *testing.T) {
	var denied bool
	now := time.Unix(1_700_000_000, 0)
	r := router(rlgin.New(newLimiter(t),
		rlgin.WithSkip(func(c *gin.Context) bool { return c.FullPath() == "/healthz" }),
		rlgin.WithHeaders(middleware.HeadersXRateLimit),
		rlgin.WithNow(func() time.Time { return now }),
		rlgin.WithDenyHandler(func(c *gin.Context, res ratelimit.Result) {
			denied = true
			c.AbortWithStatus(http.StatusServiceUnavailable)
		}),
	))
	for i := 0; i < 3; i++ {
		if w := do(r, "/healthz", nil); w.Code != http.StatusOK {
			t.Fatalf("health %d: %d", i, w.Code)
		}
	}
	if w := do(r, "/users/1", nil); w.Header().Get("X-RateLimit-Reset") != "1700000060" {
		t.Fatalf("X-RateLimit-Reset = %q", w.Header().Get("X-RateLimit-Reset"))
	}
	if w := do(r, "/users/1", nil); w.Code != http.StatusServiceUnavailable || !denied {
		t.Fatalf("custom deny: %d", w.Code)
	}
}

func TestKeyFuncs(t *testing.T) {
	trusted, _ := middleware.NewIPResolver(middleware.TrustProxies("10.0.0.0/8"))
	tests := []struct {
		name    string
		fn      rlgin.KeyFunc
		mod     func(*http.Request)
		want    string
		wantErr error
	}{
		{"ip", rlgin.KeyByIP(nil), nil, "192.0.2.1", nil},
		{"ip behind proxy", rlgin.KeyByIP(trusted), func(r *http.Request) {
			r.RemoteAddr = "10.0.0.1:1"
			r.Header.Set("X-Forwarded-For", "198.51.100.7")
		}, "198.51.100.7", nil},
		{"bad ip", rlgin.KeyByIP(nil), func(r *http.Request) { r.RemoteAddr = "@" }, "", middleware.ErrNoClientIP},
		{"header", rlgin.KeyByHeader("X-API-Key"), func(r *http.Request) { r.Header.Set("X-API-Key", "k1") }, "k1", nil},
		{"header missing", rlgin.KeyByHeader("X-API-Key"), nil, "", middleware.ErrMissingKey},
		{"route and ip", rlgin.KeyByRouteAndIP(nil), nil, "GET /users/:id|192.0.2.1", nil},
		{"route and bad ip", rlgin.KeyByRouteAndIP(nil), func(r *http.Request) { r.RemoteAddr = "@" }, "", middleware.ErrNoClientIP},
		{"first of", rlgin.FirstOf(rlgin.KeyByHeader("X-API-Key"), rlgin.KeyByIP(nil)), nil, "192.0.2.1", nil},
		{"first of none", rlgin.FirstOf(), nil, "", middleware.ErrMissingKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			var err error
			r := router(func(c *gin.Context) { got, err = tt.fn(c) })
			do(r, "/users/7", tt.mod)
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
	// Unrouted request falls back to the path.
	var got string
	e := gin.New()
	e.Use(func(c *gin.Context) { got, _ = rlgin.KeyByRouteAndIP(nil)(c) })
	do(e, "/nope", nil)
	if got != "GET /nope|192.0.2.1" {
		t.Fatalf("fallback key %q", got)
	}
}

type failingStore struct{}

func (failingStore) Take(context.Context, string, []ratelimit.Rule, int, time.Time) (ratelimit.Result, error) {
	return ratelimit.Result{}, errors.New("down")
}
func (failingStore) Reset(context.Context, string, []ratelimit.Rule) error { return nil }

func TestErrors(t *testing.T) {
	lim, _ := ratelimit.New(ratelimit.PerSecond(1), ratelimit.WithStore(failingStore{}), ratelimit.WithFailurePolicy(ratelimit.FailClosed))
	if w := do(router(rlgin.New(lim)), "/users/1", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("fail closed: %d", w.Code)
	}
	r := router(rlgin.New(lim, rlgin.WithErrorHandler(func(c *gin.Context, err error) { c.AbortWithStatus(http.StatusTeapot) })))
	if w := do(r, "/users/1", nil); w.Code != http.StatusTeapot {
		t.Fatalf("custom error: %d", w.Code)
	}
}
