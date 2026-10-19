// Package rlgrpc provides gRPC server interceptors for go-ratelimit.
//
//	srv := grpc.NewServer(
//		grpc.ChainUnaryInterceptor(rlgrpc.UnaryServerInterceptor(lim)),
//		grpc.ChainStreamInterceptor(rlgrpc.StreamServerInterceptor(lim)),
//	)
//
// A denied call fails with codes.ResourceExhausted and a
// google.rpc.RetryInfo detail. Rate limit headers are sent as response
// metadata with lowercase names ("ratelimit", "retry-after", ...). Streams are
// limited when they are opened, not per message.
package rlgrpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
)

// KeyFunc extracts the rate limit key from a call. fullMethod is
// "/package.Service/Method".
type KeyFunc func(ctx context.Context, fullMethod string) (string, error)

// KeyByPeerIP keys calls by client IP. With a resolver that trusts your
// proxies, the client address is read from the x-forwarded-for metadata.
func KeyByPeerIP(res *middleware.IPResolver) KeyFunc {
	return func(ctx context.Context, _ string) (string, error) {
		p, ok := peer.FromContext(ctx)
		if !ok || p.Addr == nil {
			return "", middleware.ErrNoClientIP
		}
		md, _ := metadata.FromIncomingContext(ctx)
		ip, err := res.ClientIP(p.Addr.String(), md.Get)
		if err != nil {
			return "", err
		}
		return ip.String(), nil
	}
}

// KeyByMetadata keys calls by an incoming metadata value, such as an API key.
func KeyByMetadata(name string) KeyFunc {
	return func(ctx context.Context, _ string) (string, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		if v := md.Get(name); len(v) > 0 && v[0] != "" {
			return v[0], nil
		}
		return "", middleware.ErrMissingKey
	}
}

// KeyByContextValue keys calls by a context value set by an earlier
// interceptor, such as an authenticated user ID.
func KeyByContextValue(key any) KeyFunc {
	return func(ctx context.Context, _ string) (string, error) {
		v := ctx.Value(key)
		if v == nil || v == "" {
			return "", middleware.ErrMissingKey
		}
		return fmt.Sprint(v), nil
	}
}

// KeyByMethodAndIP keys calls by full method name and client IP.
func KeyByMethodAndIP(res *middleware.IPResolver) KeyFunc {
	ip := KeyByPeerIP(res)
	return func(ctx context.Context, fullMethod string) (string, error) {
		k, err := ip(ctx, fullMethod)
		if err != nil {
			return "", err
		}
		return fullMethod + "|" + k, nil
	}
}

// FirstOf returns the first key produced without error.
func FirstOf(fns ...KeyFunc) KeyFunc {
	return func(ctx context.Context, fullMethod string) (string, error) {
		err := middleware.ErrMissingKey
		for _, fn := range fns {
			var k string
			if k, err = fn(ctx, fullMethod); err == nil {
				return k, nil
			}
		}
		return "", err
	}
}

// Option configures the interceptors.
type Option func(*config)

type config struct {
	key     KeyFunc
	headers middleware.HeaderStyle
	skip    func(ctx context.Context, fullMethod string) bool
	deny    func(ctx context.Context, res ratelimit.Result) error
	onError func(ctx context.Context, err error) error
	now     func() time.Time
}

// WithKeyFunc sets how calls are keyed. The default is KeyByPeerIP(nil).
func WithKeyFunc(fn KeyFunc) Option { return func(c *config) { c.key = fn } }

// WithHeaders selects the rate limit metadata (default middleware.HeadersIETF).
func WithHeaders(style middleware.HeaderStyle) Option { return func(c *config) { c.headers = style } }

// WithSkip bypasses the limiter when skip returns true, e.g. for
// "/grpc.health.v1.Health/Check".
func WithSkip(skip func(ctx context.Context, fullMethod string) bool) Option {
	return func(c *config) { c.skip = skip }
}

// WithDenyHandler sets the error returned for denied calls. The default is
// DenyError.
func WithDenyHandler(fn func(ctx context.Context, res ratelimit.Result) error) Option {
	return func(c *config) { c.deny = fn }
}

// WithErrorHandler sets the error returned when the key cannot be extracted
// or the limiter fails closed. The default is ErrorStatus.
func WithErrorHandler(fn func(ctx context.Context, err error) error) Option {
	return func(c *config) { c.onError = fn }
}

// WithNow sets the time source for the absolute x-ratelimit-reset value.
func WithNow(now func() time.Time) Option { return func(c *config) { c.now = now } }

// DenyError is a ResourceExhausted status with a RetryInfo detail.
func DenyError(_ context.Context, res ratelimit.Result) error {
	st := status.New(codes.ResourceExhausted, "rate limit exceeded")
	if d, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(res.RetryAfter)}); err == nil {
		st = d
	}
	return st.Err()
}

// ErrorStatus maps errors to statuses: InvalidArgument for a missing key,
// Unavailable when the limiter fails closed, Internal otherwise.
func ErrorStatus(_ context.Context, err error) error {
	switch {
	case errors.Is(err, middleware.ErrMissingKey):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ratelimit.ErrStore):
		return status.Error(codes.Unavailable, "rate limiter unavailable")
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

type limiter struct {
	lim ratelimit.Limiter
	cfg config
	hw  *middleware.HeaderWriter
}

func newLimiter(lim ratelimit.Limiter, opts []Option) *limiter {
	cfg := config{key: KeyByPeerIP(nil), headers: middleware.DefaultHeaders, deny: DenyError, onError: ErrorStatus, now: time.Now}
	for _, o := range opts {
		o(&cfg)
	}
	return &limiter{lim: lim, cfg: cfg, hw: middleware.NewHeaderWriter(lim, cfg.headers)}
}

// check rate limits a call and sends the headers with setHeader.
func (l *limiter) check(ctx context.Context, fullMethod string, setHeader func(metadata.MD) error) error {
	if l.cfg.skip != nil && l.cfg.skip(ctx, fullMethod) {
		return nil
	}
	key, err := l.cfg.key(ctx, fullMethod)
	if err != nil {
		return l.cfg.onError(ctx, err)
	}
	res, err := l.lim.Allow(ctx, key)
	if err != nil {
		return l.cfg.onError(ctx, err)
	}
	md := metadata.MD{}
	l.hw.Write(func(name, value string) { md.Set(strings.ToLower(name), value) }, res, l.cfg.now())
	_ = setHeader(md) // fails only if headers were already sent; nothing to do then
	if !res.Allowed {
		return l.cfg.deny(ctx, res)
	}
	return nil
}

// UnaryServerInterceptor rate limits unary calls.
func UnaryServerInterceptor(lim ratelimit.Limiter, opts ...Option) grpc.UnaryServerInterceptor {
	l := newLimiter(lim, opts)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := l.check(ctx, info.FullMethod, func(md metadata.MD) error { return grpc.SetHeader(ctx, md) }); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamServerInterceptor rate limits the opening of streams.
func StreamServerInterceptor(lim ratelimit.Limiter, opts ...Option) grpc.StreamServerInterceptor {
	l := newLimiter(lim, opts)
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := l.check(ss.Context(), info.FullMethod, ss.SetHeader); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}
