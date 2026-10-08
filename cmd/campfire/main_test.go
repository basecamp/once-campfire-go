package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/engine"
	"github.com/basecamp/once-campfire-go/internal/front"
)

func gzipPayload(t *testing.T, plain string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(plain)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func gunzipPayload(t *testing.T, payload []byte) string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(decoded)
}

func gzipRequest(path string) *http.Request {
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("Accept-Encoding", "gzip")
	return request
}

func TestRootConfigSkipsDeflate(t *testing.T) {
	config := rootConfig(front.FromLookup(func(string) (string, bool) { return "", false }))
	if !config.SkipDeflate {
		t.Fatal("rootConfig must skip front.Deflate for the precomposed root")
	}
}

// TestBuildRootFallbackGzip pins the fallback half of the composition: a
// legacy response through the composed root is gzipped exactly once and is
// byte-identical to a direct front.Deflate(app) call.
func TestBuildRootFallbackGzip(t *testing.T) {
	body := "<html>legacy body</html>"
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, body)
	})
	direct := front.Deflate(app)
	for _, mode := range []engine.Mode{engine.ModeOn, engine.ModeOff} {
		t.Run(mode.String(), func(t *testing.T) {
			root := buildRoot(app, mode)
			through := httptest.NewRecorder()
			root.ServeHTTP(through, gzipRequest("/"))
			want := httptest.NewRecorder()
			direct.ServeHTTP(want, gzipRequest("/"))

			if through.Code != want.Code {
				t.Fatalf("status = %d, direct = %d", through.Code, want.Code)
			}
			if got := through.Header().Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", got)
			}
			if !bytes.Equal(through.Body.Bytes(), want.Body.Bytes()) {
				t.Fatalf("gzip body changed: %d bytes through root, %d direct", through.Body.Len(), want.Body.Len())
			}
			if decoded := gunzipPayload(t, through.Body.Bytes()); decoded != body {
				t.Fatalf("decoded body = %q, want exactly one gzip layer", decoded)
			}
		})
	}
}

// TestBuildRootOwnedEncodingNotReencoded pins the takeover half of the
// composition: buildRoot adds no encoding of its own, so an engine-owned
// pre-encoded response passes through byte for byte. The engine's own
// owned-route bypass is pinned in internal/engine (probe routes cannot be
// registered from this package on purpose).
func TestBuildRootOwnedEncodingNotReencoded(t *testing.T) {
	member := gzipPayload(t, "engine body")
	restore := engineRoot
	engineRoot = func(http.Handler, engine.Mode) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Vary", "Accept-Encoding")
			w.Write(member)
		})
	}
	t.Cleanup(func() { engineRoot = restore })

	root := buildRoot(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "legacy body")
	}), engine.ModeForce)
	response := httptest.NewRecorder()
	root.ServeHTTP(response, gzipRequest("/probe"))

	if got := response.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := response.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
	if !bytes.Equal(response.Body.Bytes(), member) {
		t.Fatalf("owned body re-encoded: %d bytes in, %d out", len(member), response.Body.Len())
	}
}
