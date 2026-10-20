package front

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

// CompletedResponse is finite application output, not a file or upgraded socket.
// Parts may borrow a request buffer until synchronous emission finishes. HTTP
// caches must copy those bytes before retaining them. Cookies/security headers
// remain on the live writer, outside the retained representation.
type CompletedResponse struct {
	Status    int
	Parts     []responsebody.Part
	Exception bool
}

func (response CompletedResponse) size() int {
	size := 0
	for _, part := range response.Parts {
		size += part.Len()
	}
	return size
}

// PrepareResponse establishes validators, coding and selected-body length before
// committing headers. The private compressor supplies its request-owned policy
// and exact-byte memo; writers without that adapter emit identity representations.
func PrepareResponse(w http.ResponseWriter, r *http.Request, response CompletedResponse) (CompletedResponse, error) {
	if response.Status == 0 {
		response.Status = http.StatusOK
	}
	h := w.Header()
	size := response.size()
	digested := false
	if !response.Exception && (response.Status == 200 || response.Status == 201) && size > 0 && h.Get("ETag") == "" && h.Get("Last-Modified") == "" {
		digest := response.Parts[0].Digest()
		if len(response.Parts) > 1 {
			digest = responsebody.Digest(response.Parts)
		}
		h.Set("ETag", fmt.Sprintf("W/\"%x\"", digest[:16]))
		digested = true
	}
	if !response.Exception && h.Get("Cache-Control") == "" {
		value := "no-cache"
		if digested {
			value = "max-age=0, private, must-revalidate"
		}
		h.Set("Cache-Control", value)
	}
	if h.Get("Content-Type") == "" && response.Status != 204 && response.Status != 304 {
		h.Set("Content-Type", "text/html; charset=utf-8")
	}

	compressor, _ := w.(*gzipResponse)
	if compressor != nil && compressor.status != 0 {
		panic("completed response prepared after streaming output")
	}
	code := compressor != nil && response.Status >= 200 && response.Status != 204 && response.Status != 304 &&
		!strings.Contains(h.Get("Cache-Control"), "no-transform") &&
		(h.Get("Content-Encoding") == "" || h.Get("Content-Encoding") == "identity") && size > 0 && h.Get("Content-Length") != "0"
	if code {
		addVary(h, "Accept-Encoding")
		if compressor.selected == "" {
			response.Status = http.StatusNotAcceptable
			h.Set("Content-Type", "text/plain")
			response.Parts = []responsebody.Part{responsebody.NewPart([]byte(fmt.Sprintf("An acceptable encoding for the requested resource %s could not be found.", r.URL.RequestURI())))}
			code = false
		} else if compressor.selected == "gzip" {
			h.Set("Content-Encoding", "gzip")
		}
	}
	if response.Status == http.StatusOK {
		modified, _ := http.ParseTime(h.Get("Last-Modified"))
		if Fresh(r, h.Get("ETag"), modified) {
			response.Status = http.StatusNotModified
			response.Parts = nil
		}
	}
	if response.Status == 204 || response.Status == 304 {
		h.Del("Content-Length")
		if response.Status == 304 {
			h.Del("Content-Type")
		}
		response.Parts = nil
		return response, nil
	}
	if code && compressor.selected == "gzip" {
		mtime := uint32(0)
		if stamp, err := http.ParseTime(h.Get("Last-Modified")); err == nil && stamp.Unix() > 0 {
			mtime = uint32(stamp.Unix())
		}
		var body []byte
		var err error
		if (r.Method == "GET" || r.Method == "HEAD") && (response.Status == 200 || response.Status == 201) &&
			size >= 1024 && size <= gzipMaxBody && compressor.cache.capacity > 0 &&
			!strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-store") {
			body, err = compressor.cache.prepare(r.Context(), response.Parts, mtime)
		} else {
			body, err = gzipBody(response.Parts, mtime)
		}
		if err != nil {
			return response, err
		}
		response.Parts = []responsebody.Part{responsebody.NewPart(body)}
	}
	h.Set("Content-Length", strconv.Itoa(response.size()))
	return response, nil
}

// EmitResponse bypasses streaming coding after completed coding has selected the
// exact bytes. Marking completion also prevents an appended empty gzip member.
func EmitResponse(w http.ResponseWriter, r *http.Request, response CompletedResponse) error {
	if compressor, ok := w.(*gzipResponse); ok {
		compressor.status = response.Status
		compressor.complete = true
		w = compressor.ResponseWriter
	}
	w.WriteHeader(response.Status)
	if r.Method == "HEAD" || response.Status == 204 || response.Status == 304 {
		return nil
	}
	for _, part := range response.Parts {
		if _, err := part.WriteTo(w); err != nil {
			return err
		}
	}
	return nil
}

// Fresh is shared by finite application responses and file-specific emitters.
// A supplied entity-tag condition takes precedence over modification dates.
func Fresh(r *http.Request, etag string, modified time.Time) bool {
	if r.Method != "GET" && r.Method != "HEAD" {
		return false
	}
	if value, ok := r.Header["If-None-Match"]; ok {
		for _, line := range value {
			for _, tag := range strings.Split(line, ",") {
				if strings.TrimSpace(tag) == etag || strings.TrimSpace(tag) == "*" {
					return true
				}
			}
		}
		return false
	}
	if since, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && !modified.IsZero() {
		return !since.Before(modified.Truncate(time.Second))
	}
	return false
}
