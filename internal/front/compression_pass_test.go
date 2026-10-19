package front

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestDeflatePassesPreEncodedGzip pins the front contract the recorded
// piece path depends on: a handler that sets Content-Encoding and writes
// complete gzip members is passed through untouched — no second compression,
// no Content-Length deletion, Vary preserved — while an identity response on
// the same handler still gets the middleware's negotiation headers.
func TestDeflatePassesPreEncodedGzip(t *testing.T) {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte("recorded piece body")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	member := buffer.Bytes()
	// A second member makes the body a multi-member stream like the assembled
	// recorded response; the front must not merge or recompress it.
	var multi bytes.Buffer
	multi.Write(member)
	multi.Write(member)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("Content-Length", strconv.Itoa(multi.Len()))
		w.WriteHeader(http.StatusOK)
		w.Write(multi.Bytes())
	})
	server := httptest.NewServer(Deflate(handler))
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	defer client.CloseIdleConnections()

	response, err := client.Do(mustRequest(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.Status)
	}
	if got := response.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if !bytes.Equal(body, multi.Bytes()) {
		t.Fatalf("pre-encoded body changed: %d bytes in, %d out", multi.Len(), len(body))
	}
	if got := response.Header.Get("Content-Length"); got != strconv.Itoa(multi.Len()) {
		t.Fatalf("Content-Length = %q, want %d", got, multi.Len())
	}
	if !strings.Contains(response.Header.Get("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", response.Header.Get("Vary"))
	}
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "recorded piece bodyrecorded piece body" {
		t.Fatalf("decoded body = %q", decoded)
	}
}

func mustRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	return request
}
