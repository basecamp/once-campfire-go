package front

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestPublicCompressionStreamingAndJitter(t *testing.T) {
	body := strings.Repeat("hello campfire ", 20000)
	for _, coding := range []string{"gzip", "zstd"} {
		t.Run(coding, func(t *testing.T) {
			handler := PublicCompression(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				for start := 0; start < len(body); start += 733 {
					io.WriteString(w, body[start:min(start+733, len(body))])
				}
			}), Config{Gzip: true, CompressionJitter: 32})
			request := httptest.NewRequest("GET", "/", nil)
			request.Header.Set("Accept-Encoding", coding)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Header().Get("Content-Encoding") != coding {
				t.Fatal(response.Header())
			}
			var decoded []byte
			var err error
			if coding == "gzip" {
				reader, e := gzip.NewReader(bytes.NewReader(response.Body.Bytes()))
				if e != nil {
					t.Fatal(e)
				}
				if reader.Comment != string(jitterFor([]byte(body), 32)) {
					t.Fatalf("jitter: %q", reader.Comment)
				}
				decoded, err = io.ReadAll(reader)
				reader.Close()
			} else {
				command := exec.Command("zstd", "-d", "-q", "-c")
				command.Stdin = bytes.NewReader(response.Body.Bytes())
				decoded, err = command.Output()
				padding := jitterFor([]byte(body), 32)
				if !bytes.HasSuffix(response.Body.Bytes(), padding) {
					t.Fatal("missing skippable jitter frame")
				}
			}
			if err != nil || string(decoded) != body {
				t.Fatalf("decode: %d bytes %v", len(decoded), err)
			}
		})
	}
}

func TestPublicCompressionSkipsAndNegotiation(t *testing.T) {
	cases := []struct {
		accept, contentType, body, cookie string
		guard                             bool
		want                              string
	}{
		{"gzip, zstd", "text/plain", strings.Repeat("x", 2048), "", false, "zstd"},
		{"gzip, zstd;q=0.5", "text/plain", strings.Repeat("x", 2048), "", false, "gzip"},
		{"gzip", "text/plain", "small", "", false, ""},
		{"zstd", "image/png", strings.Repeat("x", 2048), "", false, ""},
		{"zstd", "text/plain", strings.Repeat("x", 2048), "session=private", true, ""},
	}
	for _, c := range cases {
		request := httptest.NewRequest("GET", "/", nil)
		request.Header.Set("Accept-Encoding", c.accept)
		request.Header.Set("Cookie", c.cookie)
		response := httptest.NewRecorder()
		PublicCompression(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", c.contentType)
			io.WriteString(w, c.body)
		}), Config{Gzip: true, CompressionJitter: 32, DisableGzipOnAuth: c.guard}).ServeHTTP(response, request)
		if response.Header().Get("Content-Encoding") != c.want {
			t.Fatalf("%+v: %v", c, response.Header())
		}
		if !strings.Contains(response.Header().Get("Vary"), "Accept-Encoding") {
			t.Fatal(response.Header())
		}
	}
}
