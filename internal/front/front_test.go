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
	}), Config{Gzip: true})
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
func TestCacheServesCompressedAndIdentity(t *testing.T) {
	var calls atomic.Int32
	body := "<html>" + strings.Repeat("room ", 400) + "</html>"
	c := NewCache(8<<20, 1<<20)
	handler := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=30")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("ETag", `"room"`)
		io.WriteString(w, body)
	}))
	request := func(path, encoding string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if encoding != "" {
			r.Header.Set("Accept-Encoding", encoding)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	// A miss streams the handler's identity body. The compressed copies are
	// stored beside it and served on the next request, so the outer compressor
	// can skip a hit.
	first := request("/room", "gzip")
	if first.Header().Get("X-Cache") != "miss" || first.Header().Get("Content-Encoding") != "" || first.Body.String() != body {
		t.Fatal(first.Header(), first.Body.Len())
	}
	gzipped := request("/room", "gzip")
	if gzipped.Header().Get("X-Cache") != "hit" || gzipped.Header().Get("Content-Encoding") != "gzip" || calls.Load() != 1 {
		t.Fatal(gzipped.Header(), calls.Load())
	}
	reader, err := gzip.NewReader(gzipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil || string(raw) != body {
		t.Fatalf("gzip body: %q %v", raw, err)
	}
	plain := request("/room", "")
	if plain.Header().Get("X-Cache") != "hit" || plain.Header().Get("Content-Encoding") != "" || plain.Body.String() != body || calls.Load() != 1 {
		t.Fatalf("identity hit changed the body or recalled the handler: %s %d", plain.Header(), calls.Load())
	}
	if hits, misses := c.Stats(); hits != 2 || misses != 1 {
		t.Fatalf("cache stats hits=%d misses=%d", hits, misses)
	}

	small := "<html>tiny</html>"
	smallHandler := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=30")
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, small)
	}))
	r := httptest.NewRequest("GET", "/small", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	smallHandler.ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != small {
		t.Fatalf("small body was compressed: %s %q", w.Header(), w.Body.String())
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

func TestCacheRespectsCompressionVetoes(t *testing.T) {
	body := "<html>" + strings.Repeat("room ", 400) + "</html>"
	newHandler := func(gzip, disableAuth bool) (*Cache, http.Handler) {
		c := NewCache(8<<20, 1<<20).AllowCompression(gzip, disableAuth)
		return c, c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=30")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.URL.Path == "/veto" {
				w.Header().Set("No-Gzip-Compression", "1")
			}
			io.WriteString(w, body)
		}))
	}
	request := func(handler http.Handler, path, encoding, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if encoding != "" {
			r.Header.Set("Accept-Encoding", encoding)
		}
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	_, disabled := newHandler(false, false)
	request(disabled, "/off", "gzip", "")
	hit := request(disabled, "/off", "gzip", "")
	if hit.Header().Get("X-Cache") != "hit" || hit.Header().Get("Content-Encoding") != "" {
		t.Fatalf("gzip disabled still compressed: %v", hit.Header())
	}

	_, guarded := newHandler(true, true)
	request(guarded, "/auth", "gzip", "")
	withCookie := request(guarded, "/auth", "gzip", "session_token=secret")
	if withCookie.Header().Get("X-Cache") != "hit" || withCookie.Header().Get("Content-Encoding") != "" {
		t.Fatalf("DisableGzipOnAuth still compressed: %v", withCookie.Header())
	}
	without := request(guarded, "/auth", "gzip", "")
	if without.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("anonymous hit should stay compressed: %v", without.Header())
	}

	_, veto := newHandler(true, false)
	request(veto, "/veto", "gzip", "")
	vetoed := request(veto, "/veto", "gzip", "")
	if vetoed.Header().Get("X-Cache") != "hit" || vetoed.Header().Get("Content-Encoding") != "" {
		t.Fatalf("No-Gzip-Compression still compressed: %v", vetoed.Header())
	}
}

func TestDeflateRespectsCompressionVetoes(t *testing.T) {
	body := "<html>" + strings.Repeat("hello campfire ", 200) + "</html>"
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/veto" {
			w.Header().Set("No-Gzip-Compression", "1")
		}
		io.WriteString(w, body)
	})
	request := func(handler http.Handler, path, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Accept-Encoding", "gzip")
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	disabled := Deflate(app, Config{Gzip: false})
	got := request(disabled, "/", "")
	if got.Header().Get("Content-Encoding") != "" || !strings.Contains(got.Body.String(), "hello campfire") {
		t.Fatalf("gzip disabled still compressed: %v body=%q", got.Header(), got.Body.String()[:min(80, got.Body.Len())])
	}

	normal := Deflate(app, Config{Gzip: true})
	got = request(normal, "/", "")
	if got.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("enabled gzip missing Content-Encoding: %v", got.Header())
	}
	reader, err := gzip.NewReader(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil || string(decoded) != body {
		t.Fatalf("gzip body: %v %q", err, decoded[:min(40, len(decoded))])
	}

	guarded := Deflate(app, Config{Gzip: true, DisableGzipOnAuth: true})
	got = request(guarded, "/", "session_token=secret")
	if got.Header().Get("Content-Encoding") != "" || !strings.Contains(got.Body.String(), "hello campfire") {
		t.Fatalf("DisableGzipOnAuth still compressed: %v", got.Header())
	}
	got = request(guarded, "/", "")
	if got.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("anonymous request should still compress: %v", got.Header())
	}

	got = request(normal, "/veto", "")
	if got.Header().Get("Content-Encoding") != "" || got.Header().Get("No-Gzip-Compression") != "" || !strings.Contains(got.Body.String(), "hello campfire") {
		t.Fatalf("No-Gzip-Compression still compressed: %v", got.Header())
	}
}

func TestDeflateAndCacheMissWithoutGzip(t *testing.T) {
	body := "<html>" + strings.Repeat("room ", 400) + "</html>"
	var calls atomic.Int32
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=30")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, body)
	})
	cache := NewCache(8<<20, 1<<20).AllowCompression(false, false)
	handler := Deflate(cache.Handler(inner), Config{Gzip: false})
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/page", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	miss := request()
	if miss.Header().Get("X-Cache") != "miss" || miss.Header().Get("Content-Encoding") != "" || miss.Body.String() != body {
		t.Fatalf("miss with gzip disabled: %v len=%d", miss.Header(), miss.Body.Len())
	}
	hit := request()
	if hit.Header().Get("X-Cache") != "hit" || hit.Header().Get("Content-Encoding") != "" || hit.Body.String() != body || calls.Load() != 1 {
		t.Fatalf("hit with gzip disabled: %v calls=%d", hit.Header(), calls.Load())
	}
}
