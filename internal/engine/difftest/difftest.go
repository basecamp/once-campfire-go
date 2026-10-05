// Package difftest runs one request through two http.Handlers — the engine
// root and the legacy application — and returns masked results that a
// differential test compares byte for byte.
//
// Both handlers are served over their own httptest.Server so the comparison
// sees real HTTP semantics (status line, header canonicalization, default
// Content-Length handling, redirects). The client disables transparent gzip,
// so a handler's Content-Encoding bytes arrive exactly as written and an
// explicit Accept-Encoding in the request spec survives.
//
// Masked response headers (dropped from Result.Header before comparison):
//
//   - Date: set by net/http from the wall clock when the response is written;
//     two exchanges of the same request differ by construction.
//   - X-Request-Start: front.forward stamps the request arrival time per
//     exchange.
//
// Callers with other per-exchange fields append them to Pair.Mask; everything
// else is returned untouched for byte comparison.
//
// Preconditions for a meaningful comparison: both handlers must see
// equivalent state. Run them against identical database copies with the same
// configuration, and freeze wall-clock-dependent output with
// CAMPFIRE_FROZEN_TIME. Exchange always runs the engine exchange first and the
// legacy exchange second, so a non-idempotent request (a write, a one-shot
// token, a rate-limited path) must be accounted for by the caller — use a
// fresh fixture per side or replay-safe requests.
package difftest

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"
)

// Request is one differential exchange.
type Request struct {
	// Method is the HTTP method; empty means GET.
	Method string
	// Path is the request path plus optional query, e.g. "/rooms/1?before=5".
	Path string
	// Host overrides the request host. Go's http.Request ignores
	// Header["Host"]; set this field instead.
	Host string
	// Header carries request headers verbatim; the caller sets Accept-Encoding
	// explicitly when encoding is under test.
	Header http.Header
	// Body is the request body; nil means no body.
	Body []byte
}

// Result is one masked response, ready for comparison.
type Result struct {
	Status int
	Header http.Header
	Body   []byte
}

// DefaultMask lists response headers that vary between two exchanges of the
// same request and are dropped from Result.Header. See the package comment.
// Do not modify it; Pair copies it at construction.
var DefaultMask = []string{"Date", "X-Request-Start"}

// Pair holds the two test servers and the client used for every exchange.
type Pair struct {
	engine *httptest.Server
	legacy *httptest.Server
	client *http.Client
	// Mask is the set of response headers dropped from results. It starts as
	// a copy of DefaultMask; callers may append server-local fields.
	Mask []string
}

// New starts one httptest.Server per handler: engine is the handler under test,
// legacy the oracle. Close must be called when done.
func New(engine, legacy http.Handler) *Pair {
	return &Pair{
		engine: httptest.NewServer(engine),
		legacy: httptest.NewServer(legacy),
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{DisableCompression: true},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		Mask: append([]string(nil), DefaultMask...),
	}
}

// Close shuts both servers down and drops the client's idle connections.
func (p *Pair) Close() {
	p.client.CloseIdleConnections()
	p.engine.Close()
	p.legacy.Close()
}

// Exchange runs req through both handlers and returns the engine result first,
// the legacy result second. The first error aborts the pair.
func (p *Pair) Exchange(req Request) (Result, Result, error) {
	engine, err := p.run(p.engine, req)
	if err != nil {
		return Result{}, Result{}, fmt.Errorf("engine exchange: %w", err)
	}
	legacy, err := p.run(p.legacy, req)
	if err != nil {
		return Result{}, Result{}, fmt.Errorf("legacy exchange: %w", err)
	}
	return engine, legacy, nil
}

// Run is the one-shot form of Exchange for callers that do not keep a Pair.
func Run(engine, legacy http.Handler, req Request) (Result, Result, error) {
	p := New(engine, legacy)
	defer p.Close()
	return p.Exchange(req)
}

// run executes one exchange and masks the response.
func (p *Pair) run(server *httptest.Server, req Request) (Result, error) {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	request, err := http.NewRequest(method, server.URL+req.Path, body)
	if err != nil {
		return Result{}, err
	}
	if req.Host != "" {
		request.Host = req.Host
	}
	for key, values := range req.Header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := p.client.Do(request)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return Result{}, err
	}
	header := response.Header.Clone()
	for _, name := range p.Mask {
		header.Del(name)
	}
	return Result{Status: response.StatusCode, Header: header, Body: payload}, nil
}
