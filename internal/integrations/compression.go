package integrations

import (
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Net::HTTP's default request encodings and transparent response inflation.
type rubyTransport struct{ next http.RoundTripper }
type inflatedBody struct {
	original io.ReadCloser
	encoding string
	mu       sync.Mutex
	reader   io.ReadCloser
	err      error
	closed   bool
}

func (b *inflatedBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, http.ErrBodyReadAfterClose
	}
	if b.reader == nil && b.err == nil {
		var reader io.ReadCloser
		switch b.encoding {
		case "gzip", "x-gzip":
			reader, b.err = gzip.NewReader(b.original)
		case "deflate":
			reader, b.err = zlib.NewReader(b.original)
		}
		if b.err == nil {
			// Retain only a successfully initialized decoder, not a typed nil.
			b.reader = reader
		} else if errors.Is(b.err, io.EOF) {
			// An absent compressed header is truncation, not a valid empty body.
			// Returning EOF here would make io.ReadAll silently accept it.
			b.err = io.ErrUnexpectedEOF
		}
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.reader.Read(p)
}

func (b *inflatedBody) Close() error {
	// Close the source before waiting for Read: inflater initialization itself
	// reads headers and may be blocked on the network until Close unblocks it.
	err := b.original.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed && b.reader != nil {
		b.reader.Close()
	}
	b.closed = true
	return err
}
func (t rubyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	req := request.Clone(request.Context())
	if req.Header.Get("Accept-Encoding") == "" && req.Header.Get("Range") == "" {
		req.Header.Set("Accept-Encoding", "gzip;q=1.0,deflate;q=0.6,identity;q=0.3")
	}
	response, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.Method == "HEAD" || response.StatusCode == 204 || response.StatusCode == 304 {
		return response, nil
	}
	encoding := strings.ToLower(response.Header.Get("Content-Encoding"))
	switch encoding {
	case "gzip", "x-gzip", "deflate":
		response.Body = &inflatedBody{original: response.Body, encoding: encoding}
	default:
		return response, nil
	}
	response.Header.Del("Content-Encoding")
	response.Header.Del("Content-Length")
	response.ContentLength = -1
	response.Uncompressed = true
	return response, nil
}
