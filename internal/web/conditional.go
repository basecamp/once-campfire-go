package web

import (
	"crypto/sha256"
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/database"
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
	} else if since, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && !modified.IsZero() {
		fresh = !since.Before(modified.Truncate(time.Second))
	}
	if fresh {
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
	}
	return fresh
}
func messageFreshness(w http.ResponseWriter, r *http.Request, messages []database.Message) bool {
	etag, modified := messageValidators(messages, r.Header.Get("Turbo-Frame") != "")
	return setMessageFreshness(w, r, etag, modified)
}

func messageValidators(messages []database.Message, frame bool) (string, time.Time) {

	parts := make([]string, 0, len(messages)+2)
	var modified time.Time
	for _, m := range messages {
		parts = append(parts, fmt.Sprintf("messages/%d-%s", m.ID, m.UpdatedAt.UTC().Format("20060102150405.000000")))
		parts[len(parts)-1] = strings.ReplaceAll(parts[len(parts)-1], ".", "")
		if m.UpdatedAt.After(modified) {
			modified = m.UpdatedAt
		}
	}
	if frame {
		parts = append(parts, "frame")
	}
	parts = append(parts, "messages/index")
	hash := sha256.Sum256([]byte(strings.Join(parts, "/")))
	etag := fmt.Sprintf("W/\"%x\"", hash[:16])
	return etag, modified
}

func setMessageFreshness(w http.ResponseWriter, r *http.Request, etag string, modified time.Time) bool {
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	w.Header().Set("Cache-Control", "max-age=0, private, must-revalidate")
	return notModified(w, r, etag, modified)
}
