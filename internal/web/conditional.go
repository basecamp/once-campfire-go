package web

import (
	"net/http"
	"strings"
	"time"
)

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
