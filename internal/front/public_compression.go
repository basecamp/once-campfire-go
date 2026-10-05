package front

import (
	"compress/gzip"
	"encoding/binary"
	"hash/crc32"
	"io"
	"math/bits"
	"net/http"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/zstd"
)

func publicEncoding(r *http.Request) string {
	if r.Method == "HEAD" {
		return ""
	}
	quality := func(name string) float64 {
		for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
			pieces := strings.Split(part, ";")
			if !strings.EqualFold(strings.TrimSpace(pieces[0]), name) {
				continue
			}
			q := 1.0
			for _, p := range pieces[1:] {
				if value, ok := strings.CutPrefix(strings.TrimSpace(p), "q="); ok {
					q, _ = strconv.ParseFloat(value, 64)
					q = min(1, max(0, q))
				}
			}
			return q
		}
		return 0
	}
	gz, zs := quality("gzip"), quality("zstd")
	if zs > 0 && zs >= gz {
		return "zstd"
	}
	if gz > 0 {
		return "gzip"
	}
	return ""
}
func compressibleType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, part := range []string{"compress", "zip", "snappy", "lzma", "xz", "zstd", "brotli", "stuffit"} {
		if strings.Contains(value, part) {
			return false
		}
	}
	for _, prefix := range []string{"video/", "audio/", "image/jp", "image/png", "image/apng", "image/webp", "image/gif", "image/avif", "image/heic", "image/heif", "image/jxl"} {
		if strings.HasPrefix(value, prefix) {
			return false
		}
	}
	return true
}
func compressionPrivate(h http.Header) bool {
	if h.Get("Set-Cookie") != "" {
		return true
	}
	for _, value := range strings.Split(strings.ToLower(h.Get("Cache-Control")), ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(value), "=")
		if name == "private" || name == "no-store" {
			return true
		}
	}
	for _, value := range strings.Split(h.Get("Vary"), ",") {
		if strings.EqualFold(strings.TrimSpace(value), "cookie") {
			return true
		}
	}
	return false
}
func jitterFor(body []byte, n int) []byte {
	if n <= 0 {
		return nil
	}
	checksum := crc32.Checksum(body[:min(len(body), 64<<10)], crc32.MakeTable(crc32.Castagnoli))
	length := 1 + int((bits.RotateLeft32(checksum, 19)^0xab0755de)%uint32(n))
	padding := make([]byte, length)
	for i := range padding {
		padding[i] = "Padding-"[i%8]
	}
	return padding
}

type publicResponse struct {
	http.ResponseWriter
	request  *http.Request
	config   Config
	status   int
	selected string
	buffer   []byte
	started  bool
	writer   io.WriteCloser
	jitter   []byte
	err      error
}

func (w *publicResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *publicResponse) WriteHeader(status int) {
	if status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	h := w.Header()
	addVary(h, "Accept-Encoding")
	veto := h.Get("No-Gzip-Compression") != ""
	h.Del("No-Gzip-Compression")
	if w.config.DisableGzipOnAuth && compressionPrivate(h) {
		veto = true
	}
	if w.request.Method == "HEAD" || status == 204 || status == 304 || veto || h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		w.selected = ""
	}
	if n, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64); err == nil && n > 0 && n < 1024 {
		w.selected = ""
	}
	if w.selected == "" {
		w.start()
	}
}
func (w *publicResponse) start() {
	if w.started {
		return
	}
	w.started = true
	if w.status == 0 {
		w.status = 200
	}
	h := w.Header()
	if len(w.buffer) < 1024 || !compressibleType(h.Get("Content-Type")) {
		w.selected = ""
	}
	if h.Get("Content-Type") == "" && len(w.buffer) > 0 {
		h.Set("Content-Type", http.DetectContentType(w.buffer))
		if !compressibleType(h.Get("Content-Type")) {
			w.selected = ""
		}
	}
	if w.selected != "" {
		w.jitter = jitterFor(w.buffer, w.config.CompressionJitter)
		if w.selected == "zstd" {
			w.writer, w.err = zstd.NewWriter(w.ResponseWriter)
		} else {
			gz, _ := gzip.NewWriterLevel(w.ResponseWriter, 6)
			gz.Comment = string(w.jitter)
			w.writer = gz
		}
		if w.err != nil {
			return
		}
		h.Set("Content-Encoding", w.selected)
		h.Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(w.status)
	if len(w.buffer) > 0 {
		target := io.Writer(w.ResponseWriter)
		if w.writer != nil {
			target = w.writer
		}
		_, w.err = target.Write(w.buffer)
		w.buffer = nil
	}
}
func (w *publicResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if w.err != nil {
		return 0, w.err
	}
	count := 0
	if !w.started {
		want := 1024
		if w.config.CompressionJitter > 0 {
			want = 64 << 10
		}
		n := min(len(p), want-len(w.buffer))
		w.buffer = append(w.buffer, p[:n]...)
		p = p[n:]
		count = n
		if len(w.buffer) >= want {
			w.start()
		}
	}
	if w.err != nil {
		return count, w.err
	}
	if len(p) == 0 {
		return count, nil
	}
	target := io.Writer(w.ResponseWriter)
	if w.writer != nil {
		target = w.writer
	}
	n, err := target.Write(p)
	return count + n, err
}
func (w *publicResponse) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if !w.started {
		w.start()
	}
	if flusher, ok := w.writer.(interface{ Flush() error }); ok {
		w.err = flusher.Flush()
	}
	if w.err == nil {
		w.err = http.NewResponseController(w.ResponseWriter).Flush()
	}
}
func (w *publicResponse) finish() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	w.start()
	if w.writer != nil {
		err := w.writer.Close()
		if w.err == nil {
			w.err = err
		}
		if w.selected == "zstd" && w.err == nil && len(w.jitter) > 0 {
			trailer := make([]byte, 8+len(w.jitter))
			binary.LittleEndian.PutUint32(trailer, 0x184D2A50)
			binary.LittleEndian.PutUint32(trailer[4:], uint32(len(w.jitter)))
			copy(trailer[8:], w.jitter)
			_, w.err = w.ResponseWriter.Write(trailer)
		}
	}
}
func PublicCompression(next http.Handler, c Config) http.Handler {
	if !c.Gzip {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}
		selected := publicEncoding(r)
		if selected != "" && !compressionAllowed(true, c.DisableGzipOnAuth, r, nil) {
			selected = ""
		}
		response := &publicResponse{ResponseWriter: w, request: r, config: c, selected: selected}
		defer response.finish()
		next.ServeHTTP(response, r)
	})
}
