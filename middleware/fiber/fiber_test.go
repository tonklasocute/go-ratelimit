package rlfiber_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
	rlfiber "github.com/tonklasocute/go-ratelimit/middleware/fiber"
	"github.com/tonklasocute/go-ratelimit/ratelimittest"
	"github.com/tonklasocute/go-ratelimit/store/memory"
)

// app.Test connects from 0.0.0.0.
const peer = "0.0.0.0"

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

type response struct {
	code   int
	header http.Header
	body   string
}

func do(t *testing.T, app *fiber.App, path string, mod func(*http.Request)) response {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if mod != nil {
		mod(r)
	}
	res, err := app.Test(r)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	return response{res.StatusCode, res.Header, string(body)}
}

func newApp(mw ...fiber.Handler) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		if u := c.Get("X-User"); u != "" {
			c.Locals("user_id", u)
		}
		return c.Next()
	})
	for _, m := range mw {
		app.Use(m)
	}
	ok := func(c fiber.Ctx) error { return c.SendString("ok") }
	app.Get("/users/:id", ok)
	app.Get("/healthz", ok)
	return app
}

func TestAllowsThenDenies(t *testing.T) {
	app := newApp(rlfiber.New(newLimiter(t)))
	r := do(t, app, "/users/1", nil)
	if r.code != http.StatusOK || r.header.Get("RateLimit") != `"gcra_1_1m";r=0;t=60` {
		t.Fatalf("first: %d %v", r.code, r.header)
	}
	r = do(t, app, "/users/1", nil)
	if r.code != http.StatusTooManyRequests || r.header.Get("Retry-After") != "60" || r.body != "Too Many Requests" {
		t.Fatalf("second: %d %q %v", r.code, r.body, r.header)
	}
}

func TestPerUserJSON(t *testing.T) {
	app := newApp(rlfiber.New(newLimiter(t), rlfiber.WithKeyFunc(rlfiber.KeyByLocal("user_id")), rlfiber.WithJSONBody()))
	alice := func(r *http.Request) { r.Header.Set("X-User", "alice") }
	do(t, app, "/users/1", alice)
	r := do(t, app, "/users/1", alice)
	if want := `{"error":"rate limit exceeded","limit":1,"remaining":0,"retry_after":60}`; r.code != http.StatusTooManyRequests || r.body != want {
		t.Fatalf("alice second: %d %s", r.code, r.body)
	}
	if r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("content type %q", r.header.Get("Content-Type"))
	}
	if r := do(t, app, "/users/1", func(r *http.Request) { r.Header.Set("X-User", "bob") }); r.code != http.StatusOK {
		t.Fatalf("bob: %d", r.code)
	}
	if r := do(t, app, "/users/1", nil); r.code != http.StatusBadRequest {
		t.Fatalf("anonymous: %d", r.code)
	}
}

func TestSkipDenyAndHeaders(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	app := newApp(rlfiber.New(newLimiter(t),
		rlfiber.WithSkip(func(c fiber.Ctx) bool { return c.Path() == "/healthz" }),
		rlfiber.WithHeaders(middleware.HeadersXRateLimit),
		rlfiber.WithNow(func() time.Time { return now }),
		rlfiber.WithDenyHandler(func(c fiber.Ctx, res ratelimit.Result) error {
			return c.SendStatus(http.StatusServiceUnavailable)
		}),
	))
	for i := 0; i < 3; i++ {
		if r := do(t, app, "/healthz", nil); r.code != http.StatusOK {
			t.Fatalf("health %d: %d", i, r.code)
		}
	}
	if r := do(t, app, "/users/1", nil); r.header.Get("X-RateLimit-Reset") != "1700000060" {
		t.Fatalf("headers %v", r.header)
	}
	if r := do(t, app, "/users/1", nil); r.code != http.StatusServiceUnavailable {
		t.Fatalf("custom deny: %d", r.code)
	}
}

func TestRouteKeyAsRouteMiddleware(t *testing.T) {
	app := fiber.New()
	mw := rlfiber.New(newLimiter(t), rlfiber.WithKeyFunc(rlfiber.KeyByRouteAndIP(nil)))
	ok := func(c fiber.Ctx) error { return c.SendString("ok") }
	app.Get("/users/:id", mw, ok)
	app.Get("/orders", mw, ok)
	if r := do(t, app, "/users/1", nil); r.code != http.StatusOK {
		t.Fatalf("users/1: %d", r.code)
	}
	if r := do(t, app, "/users/2", nil); r.code != http.StatusTooManyRequests {
		t.Fatalf("users/2 shares the pattern: %d", r.code)
	}
	if r := do(t, app, "/orders", nil); r.code != http.StatusOK {
		t.Fatalf("orders: %d", r.code)
	}
}

func TestKeyFuncs(t *testing.T) {
	trusted, _ := middleware.NewIPResolver(middleware.TrustProxies(peer))
	tests := []struct {
		name    string
		fn      rlfiber.KeyFunc
		mod     func(*http.Request)
		want    string
		wantErr error
	}{
		{"ip", rlfiber.KeyByIP(nil), nil, peer, nil},
		{"ip behind proxy", rlfiber.KeyByIP(trusted), func(r *http.Request) {
			r.Header.Add("X-Forwarded-For", "6.6.6.6")
			r.Header.Add("X-Forwarded-For", "198.51.100.7")
		}, "198.51.100.7", nil},
		{"header", rlfiber.KeyByHeader("X-API-Key"), func(r *http.Request) { r.Header.Set("X-API-Key", "k1") }, "k1", nil},
		{"header missing", rlfiber.KeyByHeader("X-API-Key"), nil, "", middleware.ErrMissingKey},
		{"local missing", rlfiber.KeyByLocal("user_id"), nil, "", middleware.ErrMissingKey},
		{"first of", rlfiber.FirstOf(rlfiber.KeyByHeader("X-API-Key"), rlfiber.KeyByIP(nil)), nil, peer, nil},
		{"first of none", rlfiber.FirstOf(), nil, "", middleware.ErrMissingKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			var err error
			app := newApp(func(c fiber.Ctx) error {
				got, err = tt.fn(c)
				return c.Next()
			})
			do(t, app, "/users/7", tt.mod)
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
	if r := do(t, newApp(rlfiber.New(lim)), "/users/1", nil); r.code != http.StatusServiceUnavailable {
		t.Fatalf("fail closed: %d", r.code)
	}
	app := newApp(rlfiber.New(lim, rlfiber.WithErrorHandler(func(c fiber.Ctx, err error) error {
		return c.SendStatus(http.StatusTeapot)
	})))
	if r := do(t, app, "/users/1", nil); r.code != http.StatusTeapot {
		t.Fatalf("custom error: %d", r.code)
	}
}
