package front

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/httpcompat"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

var gzipPool = sync.Pool{New: func() any { writer, _ := gzip.NewWriterLevel(nil, 6); return writer }}

// encoding delegates to the shared negotiation in internal/httpcompat so the
// middleware and the application's pre-encoded responses always agree; see
// httpcompat.Encoding.
func encoding(header string) string { return httpcompat.Encoding(header) }
func addVary(h http.Header, name string) {
	for _, line := range h.Values("Vary") {
		for _, value := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(value), name) || strings.TrimSpace(value) == "*" {
				return
			}
		}
	}
	if old := h.Get("Vary"); old != "" {
		h.Set("Vary", old+","+name)
	} else {
		h.Set("Vary", name)
	}
}

type gzipResponse struct {
	http.ResponseWriter
	request  *http.Request
	writer   *gzip.Writer
	selected string
	status   int
	drop     bool
}

func (w *gzipResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *gzipResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.status = 101
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
func (w *gzipResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	h := w.Header()
	if status < 200 || status == 204 || status == 304 || strings.Contains(h.Get("Cache-Control"), "no-transform") || h.Get("Content-Encoding") != "" && h.Get("Content-Encoding") != "identity" || h.Get("Content-Length") == "0" {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	addVary(h, "Accept-Encoding")
	if w.selected == "" {
		w.drop = true
		h.Del("Content-Length")
		h.Set("Content-Type", "text/plain")
		w.ResponseWriter.WriteHeader(406)
		fmt.Fprintf(w.ResponseWriter, "An acceptable encoding for the requested resource %s could not be found.", w.request.URL.RequestURI())
		return
	}
	if w.selected == "gzip" {
		h.Set("Content-Encoding", "gzip")
		h.Del("Content-Length")
		w.writer = gzipPool.Get().(*gzip.Writer)
		w.writer.Reset(w.ResponseWriter)
		w.writer.Header.OS = 3
		if stamp, err := http.ParseTime(h.Get("Last-Modified")); err == nil {
			w.writer.Header.ModTime = stamp
		}
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *gzipResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(200)
	}
	if w.drop || w.request.Method == "HEAD" {
		return len(p), nil
	}
	if w.writer != nil {
		return w.writer.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

// Preserve io.StringWriter through the middleware so cached HTML need not be
// copied into a temporary byte slice for an identity response.
func (w *gzipResponse) WriteString(value string) (int, error) {
	if w.status == 0 {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType([]byte(value[:min(len(value), 512)])))
		}
		w.WriteHeader(200)
	}
	if w.drop || w.request.Method == "HEAD" {
		return len(value), nil
	}
	if w.writer != nil {
		return io.WriteString(w.writer, value)
	}
	return io.WriteString(w.ResponseWriter, value)
}
func (w *gzipResponse) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if w.writer != nil {
		w.writer.Flush()
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}
func Deflate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}
		wrapped := &gzipResponse{ResponseWriter: w, request: r, selected: encoding(r.Header.Get("Accept-Encoding"))}
		next.ServeHTTP(wrapped, r)
		if wrapped.status == 0 {
			wrapped.WriteHeader(200)
		}
		if wrapped.writer != nil {
			if r.Method != "HEAD" {
				wrapped.writer.Close()
			}
			wrapped.writer.Reset(nil)
			gzipPool.Put(wrapped.writer)
		}
	})
}
