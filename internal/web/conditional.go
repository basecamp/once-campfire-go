package web

import (
	"net/http"
	"strings"
	"time"
)

// requestIsFresh reports whether the request is conditionally fresh, without
// writing anything: the framed path defers the 304 to its own precomposed
// emission, while notModified writes it through the wrapper chain. The ETag
// is compared byte-wise (arena-backed on the framed path), and the
// If-Modified-Since branch matches notModified's (only when a Last-Modified
// is present, which the recorded pages never carry).
func requestIsFresh(r *http.Request, etag []byte, modified time.Time) bool {
	if r.Method != "GET" && r.Method != "HEAD" {
		return false
	}
	if value, ok := r.Header["If-None-Match"]; ok {
		for _, line := range value {
			for start := 0; start <= len(line); {
				end := strings.IndexByte(line[start:], ',')
				var token string
				if end < 0 {
					token = strings.TrimSpace(line[start:])
					start = len(line) + 1
				} else {
					token = strings.TrimSpace(line[start : start+end])
					start += end + 1
				}
				if bytesEqualFold(token, etag) || token == "*" {
					return true
				}
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

// bytesEqualFold compares two ASCII strings without allocation.
func bytesEqualFold(a string, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// notModified reports whether the request is conditionally fresh; a fresh
// request writes the 304 itself (via the wrapper chain) and returns true.
func notModified(w http.ResponseWriter, r *http.Request, etag string, modified time.Time) bool {
	if r.Method != "GET" && r.Method != "HEAD" {
		return false
	}
	fresh := false
	if value, ok := r.Header["If-None-Match"]; ok {
		for _, line := range value {
			for _, tag := range strings.Split(line, ",") {
				if strings.TrimSpace(tag) == etag || strings.TrimSpace(tag) == "*" {
					fresh = true
				}
			}
		}
	} else if value := r.Header.Get("If-Modified-Since"); value != "" && !modified.IsZero() {
		// Parse only when the date could matter: on the recorded piece path
		// modified is always zero, and http.ParseTime allocates even for "".
		if since, err := http.ParseTime(value); err == nil {
			fresh = !since.Before(modified.Truncate(time.Second))
		}
	}
	if fresh {
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
	}
	return fresh
}
