package web

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

var trustedProxies = func() []netip.Prefix {
	var nets []netip.Prefix
	for _, cidr := range []string{"127.0.0.0/8", "::1/128", "fc00::/7", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "fe80::/10"} {
		nets = append(nets, netip.MustParsePrefix(cidr))
	}
	return nets
}()

func splitHeader(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
}
func forwardedValues(value, parameter string) []string {
	var values []string
	for _, field := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' }) {
		name, value, ok := strings.Cut(field, "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), parameter) {
			values = append(values, strings.Trim(strings.TrimSpace(value), `"`))
		}
	}
	return values
}
func authorityAddress(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") {
		host, _, _ := strings.Cut(value[1:], "]")
		return host
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return value
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}
func requestRemoteIP(r *http.Request) (string, error) {
	parse := func(values []string, authority bool) []netip.Addr {
		var ips []netip.Addr
		for _, value := range values {
			if authority {
				value = authorityAddress(value)
			}
			if ip, err := netip.ParseAddr(strings.Trim(value, "[]")); err == nil {
				ips = append(ips, ip)
			}
		}
		slices.Reverse(ips)
		return ips
	}
	clients := parse(splitHeader(r.Header.Get("Client-IP")), false)
	values := forwardedValues(r.Header.Get("Forwarded"), "for")
	if len(values) == 0 {
		values = splitHeader(r.Header.Get("X-Forwarded-For"))
	}
	forwarded := parse(values, true)
	if len(clients) > 0 && len(forwarded) > 0 && !slices.Contains(forwarded, clients[len(clients)-1]) {
		return "", errors.New("IP spoofing attack")
	}
	ips := append(forwarded, clients...)
	peer, _ := netip.ParseAddr(authorityAddress(r.RemoteAddr))
	for _, ip := range append(slices.Clone(ips), peer) {
		if !ip.IsValid() {
			continue
		}
		if !slices.ContainsFunc(trustedProxies, func(prefix netip.Prefix) bool { return prefix.Contains(ip) }) {
			return ip.String(), nil
		}
	}
	if len(ips) > 0 {
		return ips[len(ips)-1].String(), nil
	}
	if peer.IsValid() {
		return peer.String(), nil
	}
	return "", nil
}
func remoteIP(r *http.Request) string { ip, _ := requestRemoteIP(r); return ip }

func (s *Server) requestHTTPS(r *http.Request) bool {
	if s.Secure || r.Header.Get("X-Forwarded-Ssl") == "on" {
		return true
	}
	values := forwardedValues(r.Header.Get("Forwarded"), "proto")
	if len(values) > 0 {
		switch values[len(values)-1] {
		case "https", "wss":
			return true
		case "http", "ws":
			return false
		}
	}
	for _, header := range []string{"X-Forwarded-Proto", "X-Forwarded-Scheme"} {
		values = splitHeader(r.Header.Get(header))
		for i := len(values) - 1; i >= 0; i-- {
			switch values[i] {
			case "https", "wss":
				return true
			case "http", "ws":
				return false
			}
		}
	}
	return r.TLS != nil || r.URL.Scheme == "https"
}
func (s *Server) origin(r *http.Request) string {
	scheme, standardPort := "http", "80"
	if s.requestHTTPS(r) {
		scheme, standardPort = "https", "443"
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); strings.TrimSpace(forwarded) != "" {
		hosts := strings.Split(forwarded, ",")
		host = strings.TrimSpace(hosts[len(hosts)-1])
	}
	if host == "" {
		host = "localhost"
	}
	if name, port, err := net.SplitHostPort(host); err == nil && port == standardPort {
		host = name
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return scheme + "://" + host
}
