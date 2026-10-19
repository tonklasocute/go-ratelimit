// Package middleware holds the framework-independent parts of the
// go-ratelimit middleware: client IP resolution with trusted proxies, rate
// limit response headers and the JSON denial body. The adapters (rlhttp,
// rlchi, rlgin, rlecho, rlfiber, rlgrpc) build on it.
package middleware

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	ratelimit "github.com/tonklasocute/go-ratelimit"
)

// HeaderStyle selects which rate limit headers are sent. Styles are bit
// flags and can be combined, e.g. HeadersIETF|HeadersXRateLimit.
type HeaderStyle uint8

const (
	// HeadersIETF sends the RateLimit-Policy and RateLimit structured fields
	// of draft-ietf-httpapi-ratelimit-headers-11:
	//
	//	RateLimit-Policy: "gcra_10_1s";q=10;w=1, "fixed_window_1000_1h";q=1000;w=3600
	//	RateLimit: "gcra_10_1s";r=7;t=1
	HeadersIETF HeaderStyle = 1 << iota
	// HeadersIETFLegacy sends RateLimit-Limit, RateLimit-Remaining and
	// RateLimit-Reset (delta seconds), as defined by earlier drafts and still
	// widely deployed.
	HeadersIETFLegacy
	// HeadersXRateLimit sends the de facto X-RateLimit-Limit,
	// X-RateLimit-Remaining and X-RateLimit-Reset (Unix time in seconds, as
	// GitHub does).
	HeadersXRateLimit

	// HeadersNone sends no rate limit headers. Retry-After is still sent on
	// denial.
	HeadersNone HeaderStyle = 0
	// DefaultHeaders is the style used when none is configured.
	DefaultHeaders = HeadersIETF
)

// ErrMissingKey is returned by key functions when a request does not carry
// the expected key, for example a missing API key header.
var ErrMissingKey = errors.New("ratelimit: missing rate limit key")

// ErrorStatus maps a middleware error to an HTTP status: 400 for
// ErrMissingKey, 503 for ratelimit.ErrStore (fail closed), 500 otherwise.
func ErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrMissingKey):
		return http.StatusBadRequest
	case errors.Is(err, ratelimit.ErrStore):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// RuleLister is implemented by limiters that can list their rules, such as
// *ratelimit.RuleLimiter. It is used to render RateLimit-Policy.
type RuleLister interface {
	Rules() []ratelimit.Rule
}

// HeaderWriter renders rate limit headers for one limiter. Create it once per
// middleware with NewHeaderWriter; it is safe for concurrent use.
type HeaderWriter struct {
	style  HeaderStyle
	policy string // precomputed RateLimit-Policy value
}

// NewHeaderWriter creates a HeaderWriter for lim. If lim implements
// RuleLister, RateLimit-Policy lists every rule; otherwise it is omitted.
func NewHeaderWriter(lim ratelimit.Limiter, style HeaderStyle) *HeaderWriter {
	w := &HeaderWriter{style: style}
	if rl, ok := lim.(RuleLister); ok {
		items := make([]string, 0, len(rl.Rules()))
		for _, r := range rl.Rules() {
			items = append(items, sfString(r.Name)+";q="+strconv.Itoa(r.Capacity())+";w="+strconv.FormatInt(policyWindow(r), 10))
		}
		w.policy = strings.Join(items, ", ")
	}
	return w
}

// Write calls set for each header describing res. now is used for the
// absolute X-RateLimit-Reset. Retry-After is set whenever res is a denial.
func (w *HeaderWriter) Write(set func(name, value string), res ratelimit.Result, now time.Time) {
	limit := strconv.Itoa(res.Limit)
	remaining := strconv.Itoa(max(res.Remaining, 0))
	reset := ceilSeconds(res.ResetAfter)
	if w.style&HeadersIETF != 0 {
		if w.policy != "" {
			set("RateLimit-Policy", w.policy)
		}
		set("RateLimit", sfString(res.Policy)+";r="+remaining+";t="+strconv.FormatInt(reset, 10))
	}
	if w.style&HeadersIETFLegacy != 0 {
		set("RateLimit-Limit", limit)
		set("RateLimit-Remaining", remaining)
		set("RateLimit-Reset", strconv.FormatInt(reset, 10))
	}
	if w.style&HeadersXRateLimit != 0 {
		set("X-RateLimit-Limit", limit)
		set("X-RateLimit-Remaining", remaining)
		set("X-RateLimit-Reset", strconv.FormatInt(now.Unix()+reset, 10))
	}
	if !res.Allowed {
		set("Retry-After", strconv.FormatInt(RetryAfterSeconds(res), 10))
	}
}

// RetryAfterSeconds is res.RetryAfter rounded up to whole seconds, at least
// 1 for a denial (Retry-After has no sub-second precision).
func RetryAfterSeconds(res ratelimit.Result) int64 {
	return max(ceilSeconds(res.RetryAfter), 1)
}

// DenyBody returns the JSON body sent with a 429 when JSON is enabled:
//
//	{"error":"rate limit exceeded","limit":10,"remaining":0,"retry_after":2}
func DenyBody(res ratelimit.Result) []byte {
	b := make([]byte, 0, 96)
	b = append(b, `{"error":"rate limit exceeded","limit":`...)
	b = strconv.AppendInt(b, int64(res.Limit), 10)
	b = append(b, `,"remaining":`...)
	b = strconv.AppendInt(b, int64(max(res.Remaining, 0)), 10)
	b = append(b, `,"retry_after":`...)
	b = strconv.AppendInt(b, RetryAfterSeconds(res), 10)
	return append(b, '}')
}

// policyWindow is the "w" of a rule in seconds: the window of a windowed
// algorithm, or the time a bucket takes to refill from empty.
func policyWindow(r ratelimit.Rule) int64 {
	d := r.Rate.Period
	if !r.Algorithm.Windowed() {
		d = time.Duration(float64(r.Rate.Period) * float64(r.Capacity()) / float64(r.Rate.Limit))
	}
	return max(ceilSeconds(d), 1)
}

func ceilSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(math.Ceil(d.Seconds()))
}

// sfString encodes s as a Structured Field string (RFC 9651): quoted, with
// backslash escapes, and non-printable characters replaced by '_'.
func sfString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20 || c > 0x7e:
			b.WriteByte('_')
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
