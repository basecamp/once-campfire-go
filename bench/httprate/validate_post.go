package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var (
	postIDs    = regexp.MustCompile(`data-message-id="([0-9]+)"`)
	postBodies = regexp.MustCompile(`bench write ([0-9]+)`)
)

// validatePost checks the complete receipt, not persistence. The returned ID and
// sequence are independently matched to SQLite by the application harness.
func validatePost(response *http.Response, body []byte, seq uint64, prefix string) (uint64, error) {
	kind, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || kind != "text/vnd.turbo-stream.html" {
		return 0, fmt.Errorf("wrong POST content type")
	}
	switch response.Header.Get("Content-Encoding") {
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		body, err = io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil {
			return 0, err
		}
		if closeErr != nil {
			return 0, closeErr
		}
	case "", "identity":
	default:
		return 0, fmt.Errorf("unexpected POST content encoding")
	}
	text := string(body)
	ids := postIDs.FindAllStringSubmatch(text, -1)
	bodies := postBodies.FindAllStringSubmatch(text, -1)
	sequence := strconv.FormatUint(seq, 10)
	if !strings.HasPrefix(text, "<turbo-stream ") || !strings.HasSuffix(strings.TrimSpace(text), "</turbo-stream>") ||
		!strings.Contains(text, `action="append"`) || !strings.Contains(text, "<template>") || !strings.Contains(text, "</template>") ||
		!strings.Contains(text, `id="message_`+prefix+sequence+`"`) || len(ids) != 1 || len(bodies) != 1 || bodies[0][1] != sequence {
		return 0, fmt.Errorf("incomplete or mismatched POST receipt")
	}
	id, err := strconv.ParseUint(ids[0][1], 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid message ID")
	}
	return id, nil
}
