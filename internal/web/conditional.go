package web

import (
	"crypto/sha256"
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/database"
	"net/http"
	"strconv"
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
	} else if !modified.IsZero() {
		if since, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil {
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
func messageFreshness(w http.ResponseWriter, r *http.Request, messages []database.Message) bool {
	key, modified := messageFreshnessKey(messages, r.Header.Get("Turbo-Frame") != "")
	hash := sha256.Sum256(key)
	etag := fmt.Sprintf("W/\"%x\"", hash[:16])
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	w.Header().Set("Cache-Control", "max-age=0, private, must-revalidate")
	return notModified(w, r, etag, modified)
}

// The cache keys of the messages (messages/ID-YYYYMMDDhhmmssUUUUUU), then the frame
// and template, joined by slashes.
func messageFreshnessKey(messages []database.Message, frame bool) ([]byte, time.Time) {
	var modified time.Time
	key := make([]byte, 0, len(messages)*40+32)
	for _, m := range messages {
		updated := m.UpdatedAt.UTC()
		key = append(key, "messages/"...)
		key = strconv.AppendInt(key, m.ID, 10)
		key = append(key, '-')
		key = updated.AppendFormat(key, "20060102150405")
		micros := updated.Nanosecond() / 1000
		for divisor := 100000; divisor > 0; divisor /= 10 {
			key = append(key, byte('0'+micros/divisor%10))
		}
		key = append(key, '/')
		if m.UpdatedAt.After(modified) {
			modified = m.UpdatedAt
		}
	}
	if frame {
		key = append(key, "frame/"...)
	}
	return append(key, "messages/index"...), modified
}
