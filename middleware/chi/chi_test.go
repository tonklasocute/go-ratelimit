package rlchi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	rlchi "github.com/tonklasocute/go-ratelimit/middleware/chi"
	rlhttp "github.com/tonklasocute/go-ratelimit/middleware/http"
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

func do(h http.Handler, method, path string) int {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "192.0.2.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func TestRouteKeysShareAPattern(t *testing.T) {
	var keys []string
	key := rlchi.KeyByRouteAndIP(nil)
	r := chi.NewRouter()
	r.Use(rlchi.New(newLimiter(t), rlhttp.WithKeyFunc(func(r *http.Request) (string, error) {
		k, err := key(r)
		keys = append(keys, k)
		return k, err
	})))
	r.Get("/users/{id}", ok)
	r.Get("/orders", ok)

	if code := do(r, http.MethodGet, "/users/1"); code != http.StatusOK {
		t.Fatalf("first: %d", code)
	}
	if code := do(r, http.MethodGet, "/users/2"); code != http.StatusTooManyRequests {
		t.Fatalf("same pattern, other id: %d", code)
	}
	if code := do(r, http.MethodGet, "/orders"); code != http.StatusOK {
		t.Fatalf("other route: %d", code)
	}
	if keys[0] != "GET /users/{id}|192.0.2.1" {
		t.Fatalf("key = %q", keys[0])
	}
	// Unknown route: falls back to the path.
	do(r, http.MethodGet, "/nope")
	if keys[3] != "GET /nope|192.0.2.1" {
		t.Fatalf("fallback key = %q", keys[3])
	}
}

func TestKeyByURLParam(t *testing.T) {
	r := chi.NewRouter()
	r.With(rlchi.New(newLimiter(t), rlhttp.WithKeyFunc(rlchi.KeyByURLParam("tenant")))).
		Get("/tenants/{tenant}/items", ok)
	if code := do(r, http.MethodGet, "/tenants/a/items"); code != http.StatusOK {
		t.Fatalf("a first: %d", code)
	}
	if code := do(r, http.MethodGet, "/tenants/a/items"); code != http.StatusTooManyRequests {
		t.Fatalf("a second: %d", code)
	}
	if code := do(r, http.MethodGet, "/tenants/b/items"); code != http.StatusOK {
		t.Fatalf("b first: %d", code)
	}
	// Without the parameter the key is missing.
	h := rlchi.New(newLimiter(t), rlhttp.WithKeyFunc(rlchi.KeyByURLParam("tenant")))(http.HandlerFunc(ok))
	if code := do(h, http.MethodGet, "/"); code != http.StatusBadRequest {
		t.Fatalf("missing param: %d", code)
	}
}

func TestRoutePatternOutsideChi(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/plain", nil)
	if got := rlchi.RoutePattern(r); got != "/plain" {
		t.Fatalf("RoutePattern = %q", got)
	}
}
