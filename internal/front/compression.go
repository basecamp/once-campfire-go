package front

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/splice"
)

func encoding(header string) string {
	type item struct {
		name       string
		q          float64
		preference int
	}
	var accepts []item
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, param, _ := strings.Cut(part, ";")
		name = strings.TrimSpace(name)
		q := 1.0
		param = strings.TrimSpace(param)
		if strings.HasPrefix(param, "q=") {
			value := strings.TrimPrefix(param, "q=")
			end := 0
			for end < len(value) && (value[end] >= '0' && value[end] <= '9' || value[end] == '.') {
				end++
			}
			if end > 0 {
				q, _ = strconv.ParseFloat(value[:end], 64)
			}
		}
		p := 2
		if name == "gzip" {
			p = 0
		} else if name == "identity" {
			p = 1
		}
		accepts = append(accepts, item{name, q, p})
		if len(accepts) == 16 {
			break
		}
	}
	var expanded []item
	wildcard := false
	for _, item := range accepts {
		if item.name != "*" {
			expanded = append(expanded, item)
			continue
		}
		if wildcard {
			continue
		}
		wildcard = true
		for _, name := range []string{"gzip", "identity"} {
			found := false
			for _, v := range accepts {
				found = found || v.name == name
			}
			if !found {
				copy := item
				copy.name = name
				expanded = append(expanded, copy)
			}
		}
	}
	rejected := map[string]bool{}
	hasIdentity := false
	for _, item := range expanded {
		if item.q == 0 {
			rejected[item.name] = true
		}
		hasIdentity = hasIdentity || item.name == "identity"
	}
	sort.SliceStable(expanded, func(i, j int) bool {
		if expanded[i].q == expanded[j].q {
			return expanded[i].preference < expanded[j].preference
		}
		return expanded[i].q > expanded[j].q
	})
	if !hasIdentity {
		expanded = append(expanded, item{name: "identity"})
	}
	for _, item := range expanded {
		if !rejected[item.name] && (item.name == "gzip" || item.name == "identity") {
			return item.name
		}
	}
	return ""
}
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
	writer   *splice.Writer
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
		stamp, err := http.ParseTime(h.Get("Last-Modified"))
		if err != nil {
			stamp = time.Time{}
		}
		w.writer = splice.NewWriter(w.ResponseWriter, stamp)
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

// WritePieces writes a body in pieces. Those deflated ahead are copied into the gzip
// member instead of being compressed again for every response.
func (w *gzipResponse) WritePieces(pieces []splice.Piece) error {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if w.drop || w.request.Method == "HEAD" {
		return nil
	}
	for _, piece := range pieces {
		var err error
		if w.writer != nil {
			err = w.writer.WritePiece(piece)
		} else {
			_, err = w.ResponseWriter.Write(piece.Plain)
		}
		if err != nil {
			return err
		}
	}
	return nil
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
		if wrapped.writer != nil && r.Method != "HEAD" {
			wrapped.writer.Close()
		}
	})
}
