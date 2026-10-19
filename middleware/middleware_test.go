package middleware_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
	"github.com/tonklasocute/go-ratelimit/store/memory"
)

func TestClientIP(t *testing.T) {
	behindLB, err := middleware.NewIPResolver(middleware.TrustProxies("10.0.0.0/8", "2001:db8::1"))
	if err != nil {
		t.Fatal(err)
	}
	cloudflare, _ := middleware.NewIPResolver(middleware.TrustProxies("173.245.48.0/20"), middleware.TrustHeader("CF-Connecting-IP"))
	v6, _ := middleware.NewIPResolver(middleware.IPv6Prefix(64))

	tests := []struct {
		name    string
		r       *middleware.IPResolver
		remote  string
		headers http.Header
		want    string
	}{
		{"nil resolver uses peer", nil, "203.0.113.7:1234", nil, "203.0.113.7"},
		{"untrusted peer ignores XFF", &middleware.IPResolver{}, "203.0.113.7:1234",
			http.Header{"X-Forwarded-For": {"1.2.3.4"}}, "203.0.113.7"},
		{"spoofed entries left of the client are ignored", behindLB, "10.0.0.3:443",
			http.Header{"X-Forwarded-For": {"6.6.6.6, 203.0.113.7, 10.0.0.2"}}, "203.0.113.7"},
		{"multiple XFF header lines", behindLB, "10.0.0.3:443",
			http.Header{"X-Forwarded-For": {"6.6.6.6", "203.0.113.7", "10.1.1.1"}}, "203.0.113.7"},
		{"trusted peer without XFF", behindLB, "10.0.0.3:443", nil, "10.0.0.3"},
		{"all hops trusted uses leftmost", behindLB, "10.0.0.3:443",
			http.Header{"X-Forwarded-For": {"10.9.9.9, 10.0.0.2"}}, "10.9.9.9"},
		{"garbage from a trusted hop falls back to peer", behindLB, "10.0.0.3:443",
			http.Header{"X-Forwarded-For": {"203.0.113.7, not-an-ip"}}, "10.0.0.3"},
		{"trusted IPv6 proxy", behindLB, "[2001:db8::1]:443",
			http.Header{"X-Forwarded-For": {"198.51.100.1"}}, "198.51.100.1"},
		{"IPv4-mapped peer is unmapped", nil, "[::ffff:198.51.100.1]:80", nil, "198.51.100.1"},
		{"bare address without port", nil, "198.51.100.1", nil, "198.51.100.1"},
		{"single-address header from trusted CDN", cloudflare, "173.245.48.5:443",
			http.Header{"Cf-Connecting-Ip": {"198.51.100.9"}}, "198.51.100.9"},
		{"single-address header ignored from untrusted peer", cloudflare, "198.51.100.200:443",
			http.Header{"Cf-Connecting-Ip": {"198.51.100.9"}}, "198.51.100.200"},
		{"invalid single-address header falls back", cloudflare, "173.245.48.5:443",
			http.Header{"Cf-Connecting-Ip": {"nope"}}, "173.245.48.5"},
		{"IPv6 grouped by /64", v6, "[2001:db8:1:2:3:4:5:6]:80", nil, "2001:db8:1:2::"},
		{"IPv4 unaffected by IPv6 prefix", v6, "198.51.100.1:80", nil, "198.51.100.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := tt.headers
			if h == nil {
				h = http.Header{}
			}
			got, err := tt.r.ClientIP(tt.remote, h.Values)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tt.want {
				t.Fatalf("ClientIP = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestClientIPErrors(t *testing.T) {
	if _, err := (&middleware.IPResolver{}).ClientIP("pipe", http.Header{}.Values); !errors.Is(err, middleware.ErrNoClientIP) {
		t.Fatalf("err = %v", err)
	}
	for _, bad := range []middleware.IPOption{
		middleware.TrustProxies("10.0.0.0/33"),
		middleware.TrustProxies("example.com"),
		middleware.IPv6Prefix(129),
	} {
		if _, err := middleware.NewIPResolver(bad); err == nil {
			t.Error("NewIPResolver accepted an invalid option")
		}
	}
}

func headerMap(w *middleware.HeaderWriter, res ratelimit.Result, now time.Time) map[string]string {
	h := map[string]string{}
	w.Write(func(k, v string) { h[k] = v }, res, now)
	return h
}

func TestHeaderWriter(t *testing.T) {
	lim, err := ratelimit.New(
		ratelimit.PerSecond(10), ratelimit.WithBurst(20),
		ratelimit.WithStore(memory.New(memory.WithCleanupInterval(0))),
		ratelimit.WithRules(ratelimit.Rule{Name: `daily "quota"`, Rate: ratelimit.Per(1000, 24*time.Hour), Algorithm: ratelimit.FixedWindow}),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	allowed := ratelimit.Result{Allowed: true, Limit: 20, Remaining: 7, ResetAfter: 1300 * time.Millisecond, Policy: "gcra_10_1s"}
	denied := ratelimit.Result{Limit: 1000, ResetAfter: time.Hour, RetryAfter: 1500 * time.Millisecond, Policy: `daily "quota"`}

	t.Run("IETF", func(t *testing.T) {
		h := headerMap(middleware.NewHeaderWriter(lim, middleware.HeadersIETF), allowed, now)
		want := map[string]string{
			"RateLimit-Policy": `"gcra_10_1s";q=20;w=2, "daily \"quota\"";q=1000;w=86400`,
			"RateLimit":        `"gcra_10_1s";r=7;t=2`,
		}
		if fmt.Sprint(h) != fmt.Sprint(want) {
			t.Fatalf("got %v\nwant %v", h, want)
		}
	})
	t.Run("all styles on denial", func(t *testing.T) {
		style := middleware.HeadersIETF | middleware.HeadersIETFLegacy | middleware.HeadersXRateLimit
		h := headerMap(middleware.NewHeaderWriter(lim, style), denied, now)
		want := map[string]string{
			"RateLimit":             `"daily \"quota\"";r=0;t=3600`,
			"RateLimit-Limit":       "1000",
			"RateLimit-Remaining":   "0",
			"RateLimit-Reset":       "3600",
			"X-RateLimit-Limit":     "1000",
			"X-RateLimit-Remaining": "0",
			"X-RateLimit-Reset":     "1700003600",
			"Retry-After":           "2",
		}
		for k, v := range want {
			if h[k] != v {
				t.Errorf("%s = %q, want %q", k, h[k], v)
			}
		}
	})
	t.Run("none still sends Retry-After", func(t *testing.T) {
		h := headerMap(middleware.NewHeaderWriter(lim, middleware.HeadersNone), denied, now)
		if len(h) != 1 || h["Retry-After"] != "2" {
			t.Fatalf("got %v", h)
		}
	})
	t.Run("limiter without rules omits the policy", func(t *testing.T) {
		h := headerMap(middleware.NewHeaderWriter(plainLimiter{}, middleware.HeadersIETF), allowed, now)
		if _, ok := h["RateLimit-Policy"]; ok || h["RateLimit"] == "" {
			t.Fatalf("got %v", h)
		}
	})
	t.Run("non-printable policy names are sanitized", func(t *testing.T) {
		h := headerMap(middleware.NewHeaderWriter(plainLimiter{}, middleware.HeadersIETF),
			ratelimit.Result{Allowed: true, Policy: "a\nb\\c"}, now)
		if h["RateLimit"] != `"a_b\\c";r=0;t=0` {
			t.Fatalf("got %q", h["RateLimit"])
		}
	})
}

type plainLimiter struct{ ratelimit.Limiter }

func TestDenyBody(t *testing.T) {
	got := string(middleware.DenyBody(ratelimit.Result{Limit: 10, Remaining: -1, RetryAfter: 10 * time.Millisecond}))
	want := `{"error":"rate limit exceeded","limit":10,"remaining":0,"retry_after":1}`
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestErrorStatus(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{middleware.ErrMissingKey, http.StatusBadRequest},
		{fmt.Errorf("%w: down", ratelimit.ErrStore), http.StatusServiceUnavailable},
		{errors.New("other"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		if got := middleware.ErrorStatus(tt.err); got != tt.want {
			t.Errorf("ErrorStatus(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}
