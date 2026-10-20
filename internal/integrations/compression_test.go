package integrations

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errPrematureBodyRead = errors.New("body read before consumer requested it")

type observedResponseBody struct {
	io.ReadCloser
	allowed atomic.Bool
	reads   atomic.Int32
	closes  atomic.Int32
	entered chan struct{}
	once    sync.Once
}

func (b *observedResponseBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	if !b.allowed.Load() {
		return 0, errPrematureBodyRead
	}
	if b.entered != nil {
		b.once.Do(func() { close(b.entered) })
	}
	return b.ReadCloser.Read(p)
}
func (b *observedResponseBody) Close() error {
	b.closes.Add(1)
	return b.ReadCloser.Close()
}

func compressedResponse(t *testing.T, body io.ReadCloser, encoding string) *http.Response {
	t.Helper()
	request, err := http.NewRequest("GET", "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (rubyTransport{roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Encoding": {encoding}, "Content-Length": {"99"}}, ContentLength: 99, Body: body}, nil
	})}).RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestResponseInflationIsLazyAndKeepsBodyErrors(t *testing.T) {
	var gz, deflate bytes.Buffer
	g := gzip.NewWriter(&gz)
	g.Write([]byte("decoded body"))
	g.Close()
	z := zlib.NewWriter(&deflate)
	z.Write([]byte("decoded body"))
	z.Close()
	for _, test := range []struct {
		name, encoding, data string
		invalid, skipRead    bool
	}{
		{"gzip", "gzip", gz.String(), false, false},
		{"alias", "x-gzip", gz.String(), false, false},
		{"deflate", "deflate", deflate.String(), false, false},
		{"unconsumed", "gzip", "", true, true},
		{"absent gzip header", "gzip", "", true, false},
		{"malformed gzip", "gzip", "not a gzip stream", true, false},
		{"absent deflate header", "deflate", "", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &observedResponseBody{ReadCloser: io.NopCloser(strings.NewReader(test.data))}
			response := compressedResponse(t, body, test.encoding)
			defer response.Body.Close()
			if body.reads.Load() != 0 || !response.Uncompressed || response.ContentLength != -1 || response.Header.Get("Content-Encoding") != "" || response.Header.Get("Content-Length") != "" {
				t.Fatal("response head did not retain the transparent, lazy-body contract")
			}
			if !test.skipRead {
				body.allowed.Store(true)
				data, err := io.ReadAll(response.Body)
				if test.invalid {
					if err == nil {
						t.Fatal("malformed compressed body was accepted")
					}
				} else if err != nil || string(data) != "decoded body" {
					t.Fatalf("decoded body %q: %v", data, err)
				}
			}
			if err := response.Body.Close(); err != nil || body.closes.Load() != 1 {
				t.Fatal("source body was not closed", err)
			}
			if test.skipRead && body.reads.Load() != 0 {
				t.Fatal("closing an unread body attempted decompression")
			}
		})
	}
}

func TestResponseBodyCloseUnblocksInflaterInitialization(t *testing.T) {
	for _, encoding := range []string{"gzip", "deflate"} {
		t.Run(encoding, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer writer.Close()
			defer reader.Close()
			body := &observedResponseBody{ReadCloser: reader, entered: make(chan struct{})}
			response := compressedResponse(t, body, encoding)
			body.allowed.Store(true)
			readDone := make(chan error, 1)
			go func() { _, err := response.Body.Read(make([]byte, 1)); readDone <- err }()
			select {
			case <-body.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("body read did not start")
			}
			closeDone := make(chan error, 1)
			go func() { closeDone <- response.Body.Close() }()
			select {
			case err := <-readDone:
				if err == nil {
					t.Fatal("closed incomplete compressed header was accepted")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Close did not unblock inflater initialization")
			}
			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Close did not finish")
			}
		})
	}
}
