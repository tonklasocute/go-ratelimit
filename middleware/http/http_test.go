package rlhttp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
	rlhttp "github.com/tonklasocute/go-ratelimit/middleware/http"
	"github.com/tonklasocute/go-ratelimit/ratelimittest"
	"github.com/tonklasocute/go-ratelimit/store/memory"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

func newLimiter(t *testing.T, opts ...ratelimit.Option) *ratelimit.RuleLimiter {
	t.Helper()
	lim, err := ratelimit.New(append([]ratelimit.Option{
		ratelimit.WithStore(memory.New(memory.WithCleanupInterval(0))),
		ratelimit.WithClock(ratelimittest.NewClock()),
	}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return lim
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func get(remote string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	r.RemoteAddr = remote
	return r
}

func TestAllowsThenDenies(t *testing.T) {
	h := rlhttp.New(newLimiter(t, ratelimit.PerMinute(2)))(ok)
	for i, wantRemaining := range []string{"1", "0"} {
		w := serve(h, get("192.0.2.1:1000"))
		if w.Code != http.StatusOK || w.Body.String() != "ok" {
			t.Fatalf("request %d: %d %q", i, w.Code, w.Body)
		}
		if got := w.Header().Get("RateLimit"); got != `"gcra_2_1m";r=`+wantRemaining+`;t=`+[]string{"30", "60"}[i] {
			t.Fatalf("request %d: RateLimit = %q", i, got)
		}
		if got := w.Header().Get("RateLimit-Policy"); got != `"gcra_2_1m";q=2;w=60` {
			t.Fatalf("RateLimit-Policy = %q", got)
		}
	}
	w := serve(h, get("192.0.2.1:2000"))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "30" {
		t.Fatalf("3rd request: %d Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	if !strings.Contains(w.Body.String(), "Too Many Requests") {
		t.Fatalf("body %q", w.Body)
	}
	// A different client has its own limit.
	if w := serve(h, get("192.0.2.2:1000")); w.Code != http.StatusOK {
		t.Fatalf("other client: %d", w.Code)
	}
}

func TestJSONBodyAndLegacyHeaders(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := rlhttp.New(newLimiter(t, ratelimit.PerMinute(1)),
		rlhttp.WithJSONBody(),
		rlhttp.WithHeaders(middleware.HeadersXRateLimit|middleware.HeadersIETFLegacy),
		rlhttp.WithNow(func() time.Time { return now }),
	)(ok)
	serve(h, get("192.0.2.1:1"))
	w := serve(h, get("192.0.2.1:1"))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if want := `{"error":"rate limit exceeded","limit":1,"remaining":0,"retry_after":60}`; w.Body.String() != want {
		t.Fatalf("body %s", w.Body)
	}
	for k, v := range map[string]string{
		"X-RateLimit-Limit": "1", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1700000060",
		"RateLimit-Limit": "1", "RateLimit-Reset": "60", "RateLimit": "",
	} {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestSkipAndDenyHandler(t *testing.T) {
	var denied ratelimit.Result
	h := rlhttp.New(newLimiter(t, ratelimit.PerMinute(1)),
		rlhttp.WithSkip(func(r *http.Request) bool { return r.URL.Path == "/healthz" }),
		rlhttp.WithDenyHandler(func(w http.ResponseWriter, _ *http.Request, res ratelimit.Result) {
			denied = res
			w.WriteHeader(http.StatusServiceUnavailable)
		}),
	)(ok)
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		if w := serve(h, r); w.Code != http.StatusOK || w.Header().Get("RateLimit") != "" {
			t.Fatalf("health check %d: %d", i, w.Code)
		}
	}
	serve(h, get("192.0.2.1:1"))
	w := serve(h, get("192.0.2.1:1"))
	if w.Code != http.StatusServiceUnavailable || denied.Allowed || w.Header().Get("Retry-After") == "" {
		t.Fatalf("custom deny: %d %+v", w.Code, denied)
	}
}

type userKey struct{}

func TestKeyFuncs(t *testing.T) {
	trusted, _ := middleware.NewIPResolver(middleware.TrustProxies("10.0.0.0/8"))
	withUser := func(v any) *http.Request {
		r := get("192.0.2.1:1")
		return r.WithContext(context.WithValue(r.Context(), userKey{}, v))
	}
	apiKey := get("192.0.2.1:1")
	apiKey.Header.Set("X-API-Key", "k-123")
	proxied := get("10.0.0.1:1")
	proxied.Header.Set("X-Forwarded-For", "198.51.100.4")

	tests := []struct {
		name    string
		fn      rlhttp.KeyFunc
		r       *http.Request
		want    string
		wantErr error
	}{
		{"ip", rlhttp.KeyByIP(nil), get("192.0.2.1:1"), "192.0.2.1", nil},
		{"ip behind trusted proxy", rlhttp.KeyByIP(trusted), proxied, "198.51.100.4", nil},
		{"ip unparseable", rlhttp.KeyByIP(nil), get("@"), "", middleware.ErrNoClientIP},
		{"header", rlhttp.KeyByHeader("X-API-Key"), apiKey, "k-123", nil},
		{"header missing", rlhttp.KeyByHeader("X-API-Key"), get("192.0.2.1:1"), "", rlhttp.ErrMissingKey},
		{"context string", rlhttp.KeyByContextValue(userKey{}), withUser("u1"), "u1", nil},
		{"context int", rlhttp.KeyByContextValue(userKey{}), withUser(42), "42", nil},
		{"context empty", rlhttp.KeyByContextValue(userKey{}), withUser(""), "", rlhttp.ErrMissingKey},
		{"context missing", rlhttp.KeyByContextValue(userKey{}), get("192.0.2.1:1"), "", rlhttp.ErrMissingKey},
		{"route and ip", rlhttp.KeyByRouteAndIP(nil, nil), get("192.0.2.1:1"), "GET /items|192.0.2.1", nil},
		{"route and ip custom route", rlhttp.KeyByRouteAndIP(func(*http.Request) string { return "items" }, nil),
			get("192.0.2.1:1"), "items|192.0.2.1", nil},
		{"route and bad ip", rlhttp.KeyByRouteAndIP(nil, nil), get("@"), "", middleware.ErrNoClientIP},
		{"first of falls back", rlhttp.FirstOf(rlhttp.KeyByHeader("X-API-Key"), rlhttp.KeyByIP(nil)),
			get("192.0.2.1:1"), "192.0.2.1", nil},
		{"first of picks first", rlhttp.FirstOf(rlhttp.KeyByHeader("X-API-Key"), rlhttp.KeyByIP(nil)),
			apiKey, "k-123", nil},
		{"first of nothing", rlhttp.FirstOf(), get("192.0.2.1:1"), "", rlhttp.ErrMissingKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn(tt.r)
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

type failingStore struct{}

func (failingStore) Take(context.Context, string, []ratelimit.Rule, int, time.Time) (ratelimit.Result, error) {
	return ratelimit.Result{}, errors.New("redis: connection refused")
}
func (failingStore) Reset(context.Context, string, []ratelimit.Rule) error { return nil }

func TestErrors(t *testing.T) {
	closed, _ := ratelimit.New(ratelimit.PerSecond(1), ratelimit.WithStore(failingStore{}), ratelimit.WithFailurePolicy(ratelimit.FailClosed))
	open, _ := ratelimit.New(ratelimit.PerSecond(1), ratelimit.WithStore(failingStore{}))
	tests := []struct {
		name string
		h    http.Handler
		want int
	}{
		{"missing key", rlhttp.New(newLimiter(t, ratelimit.PerSecond(1)), rlhttp.WithKeyFunc(rlhttp.KeyByHeader("X-API-Key")))(ok), http.StatusBadRequest},
		{"store down, fail closed", rlhttp.New(closed)(ok), http.StatusServiceUnavailable},
		{"store down, fail open", rlhttp.New(open)(ok), http.StatusOK},
		{"custom error handler", rlhttp.New(closed, rlhttp.WithErrorHandler(func(w http.ResponseWriter, _ *http.Request, err error) {
			w.WriteHeader(http.StatusTeapot)
		}))(ok), http.StatusTeapot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := serve(tt.h, get("192.0.2.1:1")); w.Code != tt.want {
				t.Fatalf("status %d, want %d", w.Code, tt.want)
			}
		})
	}
}
