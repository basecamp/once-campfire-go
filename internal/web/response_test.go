package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBufferedResponseUsesKnownLengthOverHTTP(t *testing.T) {
	body := bytes.Repeat([]byte("<p>message</p>"), 4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffered := &responseBuffer{ResponseWriter: w}
		if _, err := buffered.Write(body); err != nil {
			t.Error(err)
		}
		buffered.finish(r)
	}))
	defer server.Close()
	for _, method := range []string{"GET", "HEAD"} {
		t.Run(method, func(t *testing.T) {
			request, err := http.NewRequest(method, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := io.ReadAll(response.Body)
			if closeErr := response.Body.Close(); closeErr != nil {
				t.Error(closeErr)
			}
			if err != nil || response.ContentLength != int64(len(body)) || len(response.TransferEncoding) != 0 {
				t.Fatal("incorrect response framing", response.ContentLength, response.TransferEncoding, err)
			}
			if method == "GET" && !bytes.Equal(actual, body) || method == "HEAD" && len(actual) != 0 {
				t.Fatal("incorrect response body")
			}
		})
	}
}

func TestCompletedResponseValidators(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		request := httptest.NewRequest(method, "/up", nil)
		first := httptest.NewRecorder()
		writer := &responseBuffer{ResponseWriter: first}
		writer.Write([]byte("<p>complete"))
		writer.Write([]byte(" body</p>"))
		writer.finish(request)
		if first.Header().Get("ETag") == "" || first.Header().Get("Cache-Control") != "max-age=0, private, must-revalidate" {
			t.Fatal(first.Header())
		}
		if method == "HEAD" && first.Body.Len() != 0 {
			t.Fatal("HEAD body", first.Body.String())
		}
		request.Header.Set("If-None-Match", first.Header().Get("ETag"))
		second := httptest.NewRecorder()
		writer = &responseBuffer{ResponseWriter: second}
		writer.Write([]byte("<p>complete body</p>"))
		writer.finish(request)
		if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
			t.Fatal(second.Code, second.Body.String())
		}
		changed := httptest.NewRecorder()
		writer = &responseBuffer{ResponseWriter: changed}
		writer.Write([]byte("changed"))
		writer.finish(request)
		if changed.Code != 200 {
			t.Fatal("stale validator accepted")
		}
	}
}
