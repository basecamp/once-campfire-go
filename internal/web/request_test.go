package web

import (
	"net/http/httptest"
	"testing"
)

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
		got, err := requestRemoteIP(r)
		if got != c.expected || (err != nil) != c.spoof {
			t.Fatalf("%+v: %q %v", c, got, err)
		}
	}
	s := &Server{}
	r := httptest.NewRequest("GET", "http://internal:80/", nil)
	r.Header.Set("X-Forwarded-Host", "first.example, chat.example:443")
	r.Header.Set("X-Forwarded-Proto", "http, https")
	if got := s.origin(r); got != "https://chat.example" {
		t.Fatal(got)
	}
	r.Header.Set("Forwarded", `for="[2001:db8::123]:443";proto=http`)
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	if got := s.origin(r); got != "http://chat.example:443" {
		t.Fatal(got)
	}
	if got, err := requestRemoteIP(r); got != "2001:db8::123" || err != nil {
		t.Fatal(got, err)
	}
	r.Header.Set("X-Forwarded-Ssl", "on")
	if got := s.origin(r); got != "https://chat.example" {
		t.Fatal(got)
	}
}
