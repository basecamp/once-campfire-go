package web

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestNormalizedTransportFactsSurviveRoutingAndDerivedContexts(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest("GET", "http://internal:80/rooms/1.json?before=2", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.20, 10.0.0.1")
	r.Header.Set("X-Forwarded-Host", "chat.example:443")
	r.Header.Set("X-Forwarded-Proto", "https")
	r = s.normalizeRequest(r)
	originalTarget := r.RequestURI
	derived, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(derived)
	// Route adaptation changes the request, not its authoritative ingress facts.
	r.URL.Path = "/rooms/1"
	r.Host = "other.example"
	r.RequestURI = "/rooms/1"
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	r.Header.Set("X-Forwarded-Host", "other.example")
	r.Header.Set("X-Forwarded-Proto", "http")
	info := requestMetadata(r.Context())
	if info.host != "internal:80" || info.target != originalTarget || info.ipError != nil {
		t.Fatalf("transport observation changed: %+v", info)
	}
	if origin, ip := s.origin(r), remoteIP(r); origin != "https://chat.example" || ip != "198.51.100.20" {
		t.Fatalf("consumers reinterpreted forwarding headers: %s %s", origin, ip)
	}
}

func TestProxyRequestContracts(t *testing.T) {
	cases := []struct {
		peer, forwarded, client, expected string
		spoof                             bool
	}{
		{"127.0.0.1:1", "198.51.100.20, 10.0.0.1", "", "198.51.100.20", false},
		{"8.8.8.8:1", "198.51.100.20", "", "198.51.100.20", false},
		{"127.0.0.1:1", "10.0.0.1, 192.168.0.2", "", "10.0.0.1", false},
		{"127.0.0.1:1", "bad, 198.51.100.20:8080", "", "198.51.100.20", false},
		{"127.0.0.1:1", "198.51.100.20", "203.0.113.1", "", true},
		{"127.0.0.1:1", "198.51.100.20", "198.51.100.20", "198.51.100.20", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "http://internal/", nil)
		r.RemoteAddr = c.peer
		r.Header.Set("X-Forwarded-For", c.forwarded)
		r.Header.Set("Client-IP", c.client)
		got, err := normalizeRemoteIP(r)
		if got != c.expected || (err != nil) != c.spoof {
			t.Fatalf("%+v: %q %v", c, got, err)
		}
	}
	r := httptest.NewRequest("GET", "http://internal:80/", nil)
	r.Header.Set("X-Forwarded-Host", "first.example, chat.example:443")
	r.Header.Set("X-Forwarded-Proto", "http, https")
	if got := normalizeOrigin(r, requestHTTPS(r, false)); got != "https://chat.example" {
		t.Fatal(got)
	}
	r.Header.Set("Forwarded", `for="[2001:db8::123]:443";proto=http`)
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	if got := normalizeOrigin(r, requestHTTPS(r, false)); got != "http://chat.example:443" {
		t.Fatal(got)
	}
	if got, err := normalizeRemoteIP(r); got != "2001:db8::123" || err != nil {
		t.Fatal(got, err)
	}
	r.Header.Set("X-Forwarded-Ssl", "on")
	if got := normalizeOrigin(r, requestHTTPS(r, false)); got != "https://chat.example" {
		t.Fatal(got)
	}
}
