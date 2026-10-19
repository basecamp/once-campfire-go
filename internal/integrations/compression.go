package integrations

import (
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"
)

// Net::HTTP's default request encodings and transparent response inflation.
type rubyTransport struct {
	next               http.RoundTripper
	requestReadTimeout time.Duration
}
type inflatedBody struct {
	io.ReadCloser
	original io.Closer
}

func (b *inflatedBody) Close() error { b.ReadCloser.Close(); return b.original.Close() }
func (t rubyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	if t.requestReadTimeout > 0 {
		// A keep-alive connection's read loop can already be blocked while
		// idle. Refresh that socket deadline when this request acquires it,
		// including TLS connections, before waiting for the new response.
		// The trace is request-local: WithClientTrace composes hooks in place.
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
			info.Conn.SetReadDeadline(time.Now().Add(t.requestReadTimeout))
		}})
	}
	req := request.Clone(ctx)
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
	var inflated io.ReadCloser
	switch strings.ToLower(response.Header.Get("Content-Encoding")) {
	case "gzip", "x-gzip":
		inflated, err = gzip.NewReader(response.Body)
	case "deflate":
		inflated, err = zlib.NewReader(response.Body)
	default:
		return response, nil
	}
	if err != nil {
		response.Body.Close()
		return nil, err
	}
	response.Body = &inflatedBody{inflated, response.Body}
	response.Header.Del("Content-Encoding")
	response.Header.Del("Content-Length")
	response.ContentLength = -1
	response.Uncompressed = true
	return response, nil
}
