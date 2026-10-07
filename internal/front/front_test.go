package front

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompressionNegotiation(t *testing.T) {
	for _, c := range []struct{ header, want string }{{"", "identity"}, {"gzip", "gzip"}, {"gzip;q=0", "identity"}, {"gzip;q=.5,identity;q=.8", "identity"}, {"*;q=1", "gzip"}, {"gzip;q=0,identity;q=0", ""}, {"br", "identity"}} {
		if got := encoding(c.header); got != c.want {
			t.Errorf("%q => %q want %q", c.header, got, c.want)
		}
	}
	handler := Deflate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<h1>hello</h1>")
	}))
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal(response.Header())
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "<h1>hello</h1>" {
		t.Fatal(string(body), err)
	}
	request.Header.Set("Accept-Encoding", "identity;q=0,gzip;q=0")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 406 {
		t.Fatal(response.Code)
	}
}
func TestCacheVariantsLimitsAndCookies(t *testing.T) {
	var calls atomic.Int32
	c := NewCache(2048, 1024)
	handler := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=30")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("ETag", `"version"`)
		w.Header().Add("Set-Cookie", "session_token=secret")
		io.WriteString(w, r.URL.RequestURI()+r.Header.Get("Accept-Encoding"))
	}))
	request := func(path, encoding string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Accept-Encoding", encoding)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	first := request("/a%2Fb?x=1;x=2", "gzip")
	if first.Header().Get("Set-Cookie") != "" {
		t.Fatal("cacheable response leaked cookie")
	}
	second := request("/a%2Fb?x=1;x=2", "gzip")
	if second.Header().Get("X-Cache") != "hit" || calls.Load() != 1 {
		t.Fatal(second.Header(), calls.Load())
	}
	request("/a/b?x=1;x=2", "gzip")
	request("/a%2Fb?x=1;x=2", "identity")
	if calls.Load() != 3 {
		t.Fatal("raw path or Vary collided")
	}
	for i := 0; i < 20; i++ {
		request("/"+strings.Repeat("x", i*30), "")
	}
	if c.size > c.capacity {
		t.Fatal("cache exceeded byte bound", c.size)
	}
	large := request("/"+strings.Repeat("x", 2049), "")
	if large.Header().Get("X-Cache") != "bypass" {
		t.Fatal("large URI was cached")
	}
}
func TestPrivateCacheMissesKeepFreshHeaders(t *testing.T) {
	for _, policy := range []string{"private, max-age=30", "public, no-cache, max-age=30"} {
		t.Run(policy, func(t *testing.T) {
			var calls int
			cache := NewCache(2048, 1024)
			handler := cache.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				value := strconv.Itoa(calls)
				w.Header().Set("Cache-Control", policy)
				w.Header().Set("Set-Cookie", "request="+value)
				w.Header().Set("X-Request", value)
				io.WriteString(w, value)
			}))
			for i := 1; i <= 2; i++ {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest("GET", "/private", nil))
				value := strconv.Itoa(i)
				if w.Header().Get("Set-Cookie") != "request="+value || w.Header().Get("X-Request") != value || w.Body.String() != value {
					t.Fatal("private response reused or stripped request data", w.Header(), w.Body.String())
				}
			}
		})
	}
}

func TestHTTP2AndShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	config := FromLookup(func(string) (string, bool) { return "", false })
	config.HTTPPort = port
	config.TargetPort = port
	config.H2C = true
	config.LogRequests = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	}()
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: &http.Transport{Protocols: protocols}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	var response *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatal(response.Proto)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("shutdown hung")
	}
}

func TestTLSCertificateCacheAndHTTPRedirect(t *testing.T) {
	reserve := func() int {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		return port
	}
	c := FromLookup(func(string) (string, bool) { return "", false })
	c.HTTPPort = reserve()
	c.HTTPSPort = reserve()
	c.TargetPort = c.HTTPPort
	c.Domains = []string{"example.com"}
	c.StoragePath = t.TempDir()
	c.ACMEDirectory = "http://127.0.0.1:1/unreachable"
	c.LogRequests = false
	root := "../../reference/crates/campfire/src/integrations/testdata/tls/"
	key, err := os.ReadFile(root + "server.key")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := os.ReadFile(root + "server.pem")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(c.StoragePath, "example.com"), append(key, cert...), 0600); err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(root + "ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "secure") }))
	}()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(address)
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}}
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	var response *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Get("https://example.com:" + strconv.Itoa(c.HTTPSPort) + "/")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "secure" || response.ProtoMajor != 2 {
		t.Fatal(string(body), response.Proto, err)
	}
	response, err = client.Get("http://example.com:" + strconv.Itoa(c.HTTPPort) + "/rooms/1?q=two")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 301 || response.Header.Get("Location") != "https://example.com:"+strconv.Itoa(c.HTTPSPort)+"/rooms/1?q=two" {
		t.Fatal(response.Status, response.Header)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("TLS shutdown hung")
	}
}
