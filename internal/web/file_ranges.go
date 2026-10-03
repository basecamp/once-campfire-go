package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/httpcompat"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

func serveStorageFile(w http.ResponseWriter, r *http.Request, file *os.File, stat os.FileInfo, ct string, proxy bool) {
	size := stat.Size()
	if proxy && r.Header.Get("Range") != "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if !proxy {
		modified := stat.ModTime().UTC().Format(http.TimeFormat)
		// Rack compares the literal date and ignores If-None-Match and If-Range.
		if r.Header.Get("If-Modified-Since") == modified {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("Last-Modified", modified)
	} else if r.Header.Get("Range") == "" {
		modified := time.Date(2011, 1, 1, 0, 0, 0, 0, time.UTC)
		hash := sha256.Sum256([]byte(r.URL.RequestURI()))
		etag := fmt.Sprintf("W/\"%x\"", hash[:16])
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))
		if notModified(w, r, etag, modified) {
			return
		}
	}
	ranges := httpcompat.ByteRanges(r.Header.Get("Range"), size)
	if ranges != nil && len(ranges) == 0 || proxy && r.Header.Get("Range") != "" && ranges == nil {
		if proxy {
			w.Header().Del("Content-Type")
			w.Header().Del("Content-Disposition")
			w.WriteHeader(416)
			return
		}
		body := "Byte range unsatisfiable\n"
		w.Header().Del("Last-Modified")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(416)
		if r.Method != "HEAD" {
			io.WriteString(w, body)
		}
		return
	}
	if ranges == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		if proxy {
			w.Header().Set("Accept-Ranges", "bytes")
		}
		w.WriteHeader(200)
		if r.Method != "HEAD" {
			io.CopyN(w, file, size)
		}
		return
	}
	if proxy {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	if len(ranges) == 1 {
		start, end := ranges[0][0], ranges[0][1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(206)
		if r.Method != "HEAD" {
			io.CopyN(w, io.NewSectionReader(file, start, end-start+1), end-start+1)
		}
		return
	}
	boundary := "AaB03x"
	mimeType := "text/plain"
	typeHeader, rangeHeader := "content-type", "content-range"
	if proxy {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			http.Error(w, "Internal server error", 500)
			return
		}
		boundary = hex.EncodeToString(random[:])
		mimeType = ct
		typeHeader = "Content-Type"
		rangeHeader = "Content-Range"
		w.Header().Set("Content-Type", "multipart/byteranges; boundary="+boundary)
	}
	// DiskController overrides Rack's multipart content type with the signed type.
	headers := make([]string, len(ranges))
	length := int64(0)
	for i, span := range ranges {
		headers[i] = fmt.Sprintf("\r\n--%s\r\n%s: %s\r\n%s: bytes %d-%d/%d\r\n\r\n", boundary, typeHeader, mimeType, rangeHeader, span[0], span[1], size)
		length += int64(len(headers[i])) + span[1] - span[0] + 1
	}
	ending := "\r\n--" + boundary + "--\r\n"
	length += int64(len(ending))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(206)
	if r.Method == "HEAD" {
		return
	}
	for i, span := range ranges {
		if _, err := io.WriteString(w, headers[i]); err != nil {
			return
		}
		if _, err := io.CopyN(w, io.NewSectionReader(file, span[0], span[1]-span[0]+1), span[1]-span[0]+1); err != nil {
			return
		}
	}
	io.WriteString(w, ending)
}
