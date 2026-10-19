package web

import (
	"net/http"
	"strings"
	"time"
)

// requestIsFresh reports whether the request is conditionally fresh, without
// writing anything: the framed path defers the 304 to its own precomposed
// emission, while notModified writes it through the wrapper chain. The ETag
// uses weak comparison (arena-backed on the framed path), and the
// If-Modified-Since branch matches notModified's (only when a Last-Modified
// is present, which the recorded pages never carry).
func requestIsFresh(r *http.Request, etag []byte, modified time.Time) bool {
	if r.Method != "GET" && r.Method != "HEAD" {
		return false
	}
	if value, ok := r.Header["If-None-Match"]; ok {
		for _, line := range value {
			if weakEntityTagMatch(line, etag) {
				return true
			}
		}
		return false
	}
	if value := r.Header.Get("If-Modified-Since"); value != "" && !modified.IsZero() {
		if since, err := http.ParseTime(value); err == nil {
			return !since.Before(modified.Truncate(time.Second))
		}
	}
	return false
}

// weakEntityTagMatch scans a field without splitting or copying it: commas
// inside an opaque tag are data. Like net/http's checkIfNoneMatch/scanETag,
// skip empty list members, stop at malformed tags, and compare valid tags
// after stripping W/. etag is the selected representation's valid validator.
func weakEntityTagMatch(line string, etag []byte) bool {
	if len(etag) >= 2 && etag[0] == 'W' && etag[1] == '/' {
		etag = etag[2:]
	}
	for {
		line = strings.Trim(line, " \t")
		if len(line) == 0 {
			return false
		}
		if line[0] == ',' {
			line = line[1:]
			continue
		}
		if line[0] == '*' {
			return true
		}
		line = strings.TrimPrefix(line, "W/")
		if len(line) < 2 || line[0] != '"' {
			return false
		}
		i := 1
		for ; i < len(line) && line[i] != '"'; i++ {
			c := line[i]
			if !(c == 0x21 || c >= 0x23 && c <= 0x7e || c >= 0x80) {
				return false
			}
		}
		if i == len(line) {
			return false
		}
		if line[:i+1] == string(etag) {
			return true
		}
		line = line[i+1:]
	}
}

// notModified reports whether the request is conditionally fresh; a fresh
// request writes the 304 itself (via the wrapper chain) and returns true.
func notModified(w http.ResponseWriter, r *http.Request, etag string, modified time.Time) bool {
	fresh := requestIsFresh(r, []byte(etag), modified)
	if fresh {
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
	}
	return fresh
}
