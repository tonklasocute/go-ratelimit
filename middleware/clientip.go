package middleware

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// IPResolver finds the client IP address of a request.
//
// Why forwarding headers are not trusted by default: X-Forwarded-For and
// X-Real-IP are ordinary request headers. Any client can send
// "X-Forwarded-For: 1.2.3.4" and, if the limiter believed it, get a fresh
// rate limit for every request by inventing a new address — or exhaust
// someone else's limit by spoofing theirs. A forwarding header is only
// meaningful when it was written by a proxy you operate, so IPResolver uses
// the connection's peer address unless that peer is in the trusted list.
//
// With trusted proxies, X-Forwarded-For is read from right to left: each
// proxy appends the address it received the request from, so the rightmost
// entries were written by your proxies and the first untrusted entry from the
// right is the real client. Entries further left were supplied by the client
// and are ignored.
//
//	X-Forwarded-For: 6.6.6.6, 203.0.113.7, 10.0.0.2   peer: 10.0.0.3
//	                 spoofed  client       your LB    your ingress
//
// The zero value trusts nothing and returns the peer address.
type IPResolver struct {
	trusted    []netip.Prefix
	header     string // single-address header such as CF-Connecting-IP
	ipv6Prefix int
}

// IPOption configures an IPResolver.
type IPOption func(*IPResolver) error

// TrustProxies trusts the given proxies, as IP addresses or CIDR prefixes
// (for example "10.0.0.0/8" or "2001:db8::1"). Only list proxies you run,
// such as your load balancer's subnet.
func TrustProxies(proxies ...string) IPOption {
	return func(r *IPResolver) error {
		for _, p := range proxies {
			prefix, err := parsePrefix(p)
			if err != nil {
				return err
			}
			r.trusted = append(r.trusted, prefix)
		}
		return nil
	}
}

// TrustHeader reads the client address from a header that a trusted proxy
// sets to a single address, such as "X-Real-IP", "CF-Connecting-IP" or
// "True-Client-IP", instead of X-Forwarded-For. It is only consulted when the
// peer is a trusted proxy.
func TrustHeader(name string) IPOption {
	return func(r *IPResolver) error {
		r.header = name
		return nil
	}
}

// IPv6Prefix groups IPv6 clients by a prefix, typically 64 bits: an ISP
// usually gives each customer a whole /64, so limiting single addresses lets
// one client rotate through billions of them. 0 (the default) keeps full
// addresses.
func IPv6Prefix(bits int) IPOption {
	return func(r *IPResolver) error {
		if bits < 0 || bits > 128 {
			return fmt.Errorf("middleware: invalid IPv6 prefix length %d", bits)
		}
		r.ipv6Prefix = bits
		return nil
	}
}

// NewIPResolver creates an IPResolver.
func NewIPResolver(opts ...IPOption) (*IPResolver, error) {
	r := &IPResolver{}
	for _, o := range opts {
		if err := o(r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// ErrNoClientIP is returned when the peer address cannot be parsed.
var ErrNoClientIP = errors.New("middleware: cannot determine client IP")

// ClientIP returns the client address for a connection from remoteAddr
// ("host:port" or a bare address) with request headers looked up by values,
// for example http.Header.Values.
func (r *IPResolver) ClientIP(remoteAddr string, values func(name string) []string) (netip.Addr, error) {
	peer, ok := parseAddr(remoteAddr)
	if !ok {
		return netip.Addr{}, ErrNoClientIP
	}
	if r == nil || !r.isTrusted(peer) {
		return r.mask(peer), nil
	}
	if r.header != "" {
		if v := values(r.header); len(v) > 0 {
			if ip, ok := parseAddr(strings.TrimSpace(v[len(v)-1])); ok {
				return r.mask(ip), nil
			}
		}
		return r.mask(peer), nil
	}
	return r.mask(r.fromForwardedFor(peer, values("X-Forwarded-For"))), nil
}

// fromForwardedFor walks X-Forwarded-For from the right and returns the first
// address that is not a trusted proxy.
func (r *IPResolver) fromForwardedFor(peer netip.Addr, headers []string) netip.Addr {
	client := peer
	for i := len(headers) - 1; i >= 0; i-- {
		parts := strings.Split(headers[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			ip, ok := parseAddr(strings.TrimSpace(parts[j]))
			if !ok {
				// A trusted proxy wrote garbage; do not guess further left.
				return peer
			}
			client = ip
			if !r.isTrusted(ip) {
				return ip
			}
		}
	}
	return client // every hop is trusted: the leftmost is the origin
}

func (r *IPResolver) isTrusted(ip netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func (r *IPResolver) mask(ip netip.Addr) netip.Addr {
	if r != nil && r.ipv6Prefix > 0 && ip.Is6() {
		if p, err := ip.Prefix(r.ipv6Prefix); err == nil {
			return p.Addr()
		}
	}
	return ip
}

// parseAddr accepts "ip", "ip:port", "[ipv6]:port" and "[ipv6]", and
// unmaps IPv4-mapped IPv6 addresses.
func parseAddr(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap().WithZone(""), true
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("middleware: invalid trusted proxy %q: %w", s, err)
		}
		return p.Masked(), nil
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("middleware: invalid trusted proxy %q: %w", s, err)
	}
	ip = ip.Unmap()
	return netip.PrefixFrom(ip, ip.BitLen()), nil
}
