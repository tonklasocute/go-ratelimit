package rlgrpc_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	"github.com/tonklasocute/go-ratelimit/middleware"
	rlgrpc "github.com/tonklasocute/go-ratelimit/middleware/grpc"
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

// serve starts a health server over an in-memory connection.
func serve(t *testing.T, opts ...grpc.ServerOption) healthpb.HealthClient {
	t.Helper()
	lis := bufconn.Listen(1 << 16)
	srv := grpc.NewServer(opts...)
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return healthpb.NewHealthClient(conn)
}

// fixedKey keys every call the same; bufconn peers have no IP address.
func fixedKey(context.Context, string) (string, error) { return "k", nil }

func withKey(key string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "x-api-key", key)
}

func TestUnary(t *testing.T) {
	client := serve(t, grpc.UnaryInterceptor(rlgrpc.UnaryServerInterceptor(newLimiter(t),
		rlgrpc.WithKeyFunc(rlgrpc.KeyByMetadata("x-api-key")))))

	var header metadata.MD
	if _, err := client.Check(withKey("a"), &healthpb.HealthCheckRequest{}, grpc.Header(&header)); err != nil {
		t.Fatal(err)
	}
	if got := header.Get("ratelimit"); len(got) != 1 || got[0] != `"gcra_1_1m";r=0;t=60` {
		t.Fatalf("ratelimit metadata = %v", got)
	}
	_, err := client.Check(withKey("a"), &healthpb.HealthCheckRequest{}, grpc.Header(&header))
	st := status.Convert(err)
	if st.Code() != codes.ResourceExhausted {
		t.Fatalf("second call: %v", err)
	}
	if got := header.Get("retry-after"); len(got) != 1 || got[0] != "60" {
		t.Fatalf("retry-after metadata = %v", got)
	}
	var retry *errdetails.RetryInfo
	for _, d := range st.Details() {
		if r, ok := d.(*errdetails.RetryInfo); ok {
			retry = r
		}
	}
	if retry == nil || retry.RetryDelay.AsDuration() != time.Minute {
		t.Fatalf("RetryInfo = %v", retry)
	}
	if _, err := client.Check(withKey("b"), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("other key: %v", err)
	}
	if _, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing key: %v", err)
	}
}

func TestStream(t *testing.T) {
	client := serve(t, grpc.StreamInterceptor(rlgrpc.StreamServerInterceptor(newLimiter(t), rlgrpc.WithKeyFunc(fixedKey))))
	open := func() error {
		stream, err := client.Watch(context.Background(), &healthpb.HealthCheckRequest{})
		if err != nil {
			return err
		}
		_, err = stream.Recv()
		return err
	}
	if err := open(); err != nil {
		t.Fatalf("first stream: %v", err)
	}
	if err := open(); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second stream: %v", err)
	}
}

func TestSkipAndCustomHandlers(t *testing.T) {
	closed, _ := ratelimit.New(ratelimit.PerSecond(1), ratelimit.WithStore(failingStore{}), ratelimit.WithFailurePolicy(ratelimit.FailClosed))
	client := serve(t, grpc.ChainUnaryInterceptor(
		rlgrpc.UnaryServerInterceptor(closed,
			rlgrpc.WithSkip(func(ctx context.Context, m string) bool {
				md, _ := metadata.FromIncomingContext(ctx)
				return len(md.Get("skip")) > 0
			}),
			rlgrpc.WithErrorHandler(func(ctx context.Context, err error) error {
				return status.Error(codes.Aborted, "custom")
			})),
	))
	if _, err := client.Check(metadata.AppendToOutgoingContext(context.Background(), "skip", "1"), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("skipped call: %v", err)
	}
	if _, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{}); status.Code(err) != codes.Aborted {
		t.Fatalf("custom error: %v", err)
	}

	denyClient := serve(t, grpc.UnaryInterceptor(rlgrpc.UnaryServerInterceptor(newLimiter(t),
		rlgrpc.WithKeyFunc(fixedKey),
		rlgrpc.WithHeaders(middleware.HeadersNone),
		rlgrpc.WithNow(time.Now),
		rlgrpc.WithDenyHandler(func(ctx context.Context, res ratelimit.Result) error {
			return status.Error(codes.Unavailable, "slow down")
		}))))
	denyClient.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if _, err := denyClient.Check(context.Background(), &healthpb.HealthCheckRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("custom deny: %v", err)
	}
}

type failingStore struct{}

func (failingStore) Take(context.Context, string, []ratelimit.Rule, int, time.Time) (ratelimit.Result, error) {
	return ratelimit.Result{}, errors.New("down")
}
func (failingStore) Reset(context.Context, string, []ratelimit.Rule) error { return nil }

type addr string

func (a addr) Network() string { return "tcp" }
func (a addr) String() string  { return string(a) }

type userKey struct{}

func TestKeyFuncs(t *testing.T) {
	trusted, _ := middleware.NewIPResolver(middleware.TrustProxies("10.0.0.0/8"))
	withPeer := func(a string, md ...string) context.Context {
		ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: addr(a)})
		return metadata.NewIncomingContext(ctx, metadata.Pairs(md...))
	}
	tests := []struct {
		name    string
		fn      rlgrpc.KeyFunc
		ctx     context.Context
		want    string
		wantErr error
	}{
		{"peer", rlgrpc.KeyByPeerIP(nil), withPeer("192.0.2.1:5000"), "192.0.2.1", nil},
		{"peer behind proxy", rlgrpc.KeyByPeerIP(trusted), withPeer("10.0.0.1:5000", "x-forwarded-for", "198.51.100.7"), "198.51.100.7", nil},
		{"no peer", rlgrpc.KeyByPeerIP(nil), context.Background(), "", middleware.ErrNoClientIP},
		{"bad peer", rlgrpc.KeyByPeerIP(nil), withPeer("bufconn"), "", middleware.ErrNoClientIP},
		{"metadata", rlgrpc.KeyByMetadata("x-api-key"), withPeer("192.0.2.1:1", "x-api-key", "k1"), "k1", nil},
		{"metadata missing", rlgrpc.KeyByMetadata("x-api-key"), context.Background(), "", middleware.ErrMissingKey},
		{"context value", rlgrpc.KeyByContextValue(userKey{}), context.WithValue(context.Background(), userKey{}, 7), "7", nil},
		{"context missing", rlgrpc.KeyByContextValue(userKey{}), context.Background(), "", middleware.ErrMissingKey},
		{"method and ip", rlgrpc.KeyByMethodAndIP(nil), withPeer("192.0.2.1:1"), "/svc/M|192.0.2.1", nil},
		{"method and bad ip", rlgrpc.KeyByMethodAndIP(nil), context.Background(), "", middleware.ErrNoClientIP},
		{"first of", rlgrpc.FirstOf(rlgrpc.KeyByMetadata("x-api-key"), rlgrpc.KeyByPeerIP(nil)), withPeer("192.0.2.1:1"), "192.0.2.1", nil},
		{"first of none", rlgrpc.FirstOf(), context.Background(), "", middleware.ErrMissingKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn(tt.ctx, "/svc/M")
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestErrorStatus(t *testing.T) {
	ctx := context.Background()
	if c := status.Code(rlgrpc.ErrorStatus(ctx, errors.New("x"))); c != codes.Internal {
		t.Fatalf("other error: %v", c)
	}
	if c := status.Code(rlgrpc.ErrorStatus(ctx, ratelimit.ErrStore)); c != codes.Unavailable {
		t.Fatalf("store error: %v", c)
	}
}
