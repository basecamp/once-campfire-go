package integrations

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrPrivate = errors.New("private or invalid network address")

func inRanges(ip netip.Addr, ranges []netip.Prefix) bool {
	for _, r := range ranges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

var mappedRanges = []netip.Prefix{netip.MustParsePrefix("::ffff:0:0/96"), netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b:1::/48")}
var translatedRanges = []netip.Prefix{netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("::ffff:0:0:0/96")}
var privateV6 = []netip.Prefix{netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("2001::/23")}

func Blocked(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	if ip.Is4() {
		return inRanges(ip, blockedV4)
	}
	if inRanges(ip, mappedRanges) {
		return true
	}
	if inRanges(ip, translatedRanges) {
		raw := ip.As16()
		return inRanges(netip.AddrFrom4([4]byte(raw[12:16])), blockedV4)
	}
	if inRanges(ip, ietfPublic) {
		return false
	}
	return ip.IsLoopback() || inRanges(ip, privateV6) || inRanges(ip, blockedV6) || !inRanges(ip, allocatedV6)
}

var numericHost = regexp.MustCompile(`^(?:0[xX][0-9a-fA-F]+|[0-9]+)(?:\.(?:0[xX][0-9a-fA-F]+|[0-9]+)){0,3}$`)
var hostLabel = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func numericAddress(host string) (netip.Addr, bool) {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return ip, true
	}
	if !numericHost.MatchString(host) {
		return netip.Addr{}, false
	}
	parts := strings.Split(host, ".")
	var address uint64
	for i, part := range parts {
		base := 10
		if strings.HasPrefix(strings.ToLower(part), "0x") {
			base = 16
			part = part[2:]
		} else if len(part) > 1 && part[0] == '0' {
			base = 8
			part = part[1:]
		}
		n, err := strconv.ParseUint(part, base, 32)
		if err != nil {
			return netip.Addr{}, true
		}
		if i < len(parts)-1 {
			if n > 255 {
				return netip.Addr{}, true
			}
			address |= n << uint(24-8*i)
		} else {
			bits := 32 - 8*i
			if n >= uint64(1)<<uint(bits) {
				return netip.Addr{}, true
			}
			address |= n
		}
	}
	return netip.AddrFrom4([4]byte{byte(address >> 24), byte(address >> 16), byte(address >> 8), byte(address)}), true
}

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

func ResolvePublic(ctx context.Context, resolver Resolver, host string) (netip.Addr, error) {
	if host == "" || len(host) > 255 || strings.ContainsAny(host, "%\x00") {
		return netip.Addr{}, ErrPrivate
	}
	if ip, numeric := numericAddress(host); numeric {
		if Blocked(ip) {
			return netip.Addr{}, ErrPrivate
		}
		return ip, nil
	}
	if numericHost.MatchString(strings.Trim(host, ".")) {
		return netip.Addr{}, ErrPrivate
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if !hostLabel.MatchString(label) {
			return netip.Addr{}, ErrPrivate
		}
	}
	ips, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(ips) > 256 {
		return netip.Addr{}, ErrPrivate
	}
	for _, v4 := range []bool{true, false} {
		for _, ip := range ips {
			if ip.Is4() == v4 && !Blocked(ip) {
				return ip, nil
			}
		}
	}
	return netip.Addr{}, ErrPrivate
}

type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (c *deadlineConn) Read(b []byte) (int, error) {
	c.SetReadDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(b)
}
func (c *deadlineConn) Write(b []byte) (int, error) {
	c.SetWriteDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(b)
}
func dialTimeout(timeout time.Duration, guarded bool, resolver Resolver) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if guarded {
			ip, err := ResolvePublic(ctx, resolver, host)
			if err != nil {
				return nil, err
			}
			address = net.JoinHostPort(ip.String(), port)
		}
		connection, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &deadlineConn{connection, timeout}, nil
	}
}
func HTTPClient(guarded bool, timeout time.Duration, resolver Resolver) *http.Client {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	transport := &http.Transport{TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, DisableKeepAlives: true, DisableCompression: true, DialContext: dialTimeout(timeout, guarded, resolver)}
	return &http.Client{Transport: rubyTransport{transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// WebhookHTTPClient returns the webhook delivery client: the same timeouts
// and delivery semantics as HTTPClient, with keep-alive connections so a bot
// endpoint is dialed (and its DNS looked up) once instead of once per
// delivery. The deadline wrapper still bounds every read and write on the
// reused connection.
func WebhookHTTPClient(timeout time.Duration, resolver Resolver) *http.Client {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	transport := &http.Transport{TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second, DisableCompression: true, DialContext: dialTimeout(timeout, false, resolver)}
	return &http.Client{Transport: rubyTransport{transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func PublicURL(ctx context.Context, resolver Resolver, value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" {
		return false
	}
	_, err = ResolvePublic(ctx, resolver, u.Hostname())
	return err == nil
}
