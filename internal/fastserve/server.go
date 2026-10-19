package fastserve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/sebishogun/simdhttp/http1"
)

// Server is the owned accept loop. It serves one listener with the
// application's http.Handler, parsing heads with simdhttp and writing
// responses with vectored writes, and hands protocol-switch connections
// (Upgrade heads, the HTTP/2 preface) to an internal net/http.Server.
//
// The zero value is not usable; build with New and set the timing fields, or
// use the front package's wiring (CAMPFIRE_SERVER_LOOP=on).
type Server struct {
	// Handler is the application handler, exactly the one the replaced
	// net/http server would run; both loops must see the same chain.
	Handler http.Handler

	// ReadTimeout covers reading a request including its body;
	// ReadHeaderTimeout covers the head alone and falls back to
	// ReadTimeout (net/http semantics). IdleTimeout bounds keep-alive
	// waits between requests; WriteTimeout bounds the response write.
	ReadTimeout, ReadHeaderTimeout, IdleTimeout, WriteTimeout time.Duration

	// MaxHeaderBytes caps the head; the default is net/http's 1 MiB.
	MaxHeaderBytes int64
	// MaxBodySize caps request bodies (0 = unlimited); the application's
	// own bodyLimit middleware is the primary 413 authority, this is the
	// reader-level bound.
	MaxBodySize int64
	// Protocols configures the internal handoff server; nil means HTTP/1
	// only. It must mirror the replaced listener's configuration, because
	// handed-off connections (h2c prefaces) are served under these rules.
	Protocols *http.Protocols
	// DisableGeneralOptionsHandler mirrors http.Server's flag: with it
	// false, "OPTIONS *" is answered by the loop itself.
	DisableGeneralOptionsHandler bool

	// ErrorLog receives handler-panic reports; nil logs through slog.
	ErrorLog *slog.Logger

	handoffOnce sync.Once
	handoff     *http.Server
	handoffLn   *queueListener

	mu         sync.Mutex
	conns      map[*conn]struct{}
	inShutdown bool
	ln         net.Listener // closed by Shutdown, like http.Server
	wg         sync.WaitGroup
}

// New builds a server with net/http's default limits.
func New(handler http.Handler) *Server {
	return &Server{Handler: handler, MaxHeaderBytes: http.DefaultMaxHeaderBytes}
}

func (s *Server) maxHeadBytes() int64 {
	if s.MaxHeaderBytes > 0 {
		return s.MaxHeaderBytes + 4096 // bufio slop, net/http's initialReadLimitSize
	}
	return int64(http.DefaultMaxHeaderBytes) + 4096
}

// readHeaderTimeout mirrors http.Server.readHeaderTimeout: the explicit
// value, else ReadTimeout.
func (s *Server) readHeaderTimeout() time.Duration {
	if s.ReadHeaderTimeout != 0 {
		return s.ReadHeaderTimeout
	}
	return s.ReadTimeout
}

// bodyLimits are the http1 reader's budgets: the configured body cap (or
// effectively none), the compatible profile's defaults, and a drain budget
// equal to net/http's post-handler drain so the reuse decision matches.
func (s *Server) bodyLimits() http1.Limits {
	l := http1.DefaultLimits(http1.Compatible)
	if s.MaxBodySize > 0 {
		l.MaxBodySize = s.MaxBodySize
	} else {
		l.MaxBodySize = 1 << 60
	}
	l.MaxDrainSize = maxPostHandlerReadBytes
	return l
}

// Serve accepts connections on l until Shutdown. It returns
// http.ErrServerClosed after a graceful shutdown, mirroring http.Server.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	s.ln = l
	s.mu.Unlock()
	s.initHandoff()
	for {
		rwc, err := l.Accept()
		if err != nil {
			s.mu.Lock()
			shutting := s.inShutdown
			s.mu.Unlock()
			if shutting || errors.Is(err, net.ErrClosed) {
				return http.ErrServerClosed
			}
			return err
		}
		c := newConn(s, rwc)
		s.wg.Add(1)
		s.mu.Lock()
		if s.conns == nil {
			s.conns = make(map[*conn]struct{})
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		go c.serve()
	}
}

// connDone unregisters a connection when its serve loop exits.
func (s *Server) connDone(c *conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	s.wg.Done()
}

// serveHandler runs the application handler under this loop's recover
// semantics: ErrAbortHandler closes silently, anything else is logged with a
// stack and closes (net/http parity; a panicked handler never leaves a
// reusable connection).
func (s *Server) serveHandler(res *response, req *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			if r == http.ErrAbortHandler {
				res.aborted = true
				res.brokeConn = true
				return
			}
			const size = 64 << 10
			buf := make([]byte, size)
			buf = buf[:runtime.Stack(buf, false)]
			s.logf("http: panic serving %v: %v\n%s", req.RemoteAddr, r, buf)
			res.aborted = true
			res.brokeConn = true
		}
	}()
	var h http.Handler = s.Handler
	if !s.DisableGeneralOptionsHandler && req.RequestURI == "*" && req.Method == http.MethodOptions {
		h = globalOptionsHandler{}
	}
	h.ServeHTTP(res, req)
}

func (s *Server) logf(format string, args ...any) {
	if s.ErrorLog != nil {
		s.ErrorLog.Error(fmt.Sprintf(format, args...))
		return
	}
	slog.Error(fmt.Sprintf(format, args...))
}

// initHandoff builds the internal net/http server that serves handed-off
// connections. It is the same handler with the same timing envelope, plus
// the same protocol switches, so hijacking and h2c behave identically to the
// replaced listener.
func (s *Server) initHandoff() {
	s.handoffOnce.Do(func() {
		ln := newQueueListener(handoffAddr{})
		s.handoffLn = ln
		s.handoff = &http.Server{
			Handler:           s.Handler,
			ReadTimeout:       s.ReadTimeout,
			ReadHeaderTimeout: s.readHeaderTimeout(),
			WriteTimeout:      s.WriteTimeout,
			IdleTimeout:       s.IdleTimeout,
			MaxHeaderBytes:    int(s.maxHeadBytes() - 4096),
			Protocols:         s.protocolsCopy(),
		}
		go s.handoff.Serve(ln)
	})
}

// protocolsCopy copies the Protocols configuration (the type has no public
// clone; a value copy carries the unexported bits).
func (s *Server) protocolsCopy() *http.Protocols {
	if s.Protocols == nil {
		p := new(http.Protocols)
		p.SetHTTP1(true)
		return p
	}
	p := new(http.Protocols)
	*p = *s.Protocols
	return p
}

// handoffConn pushes one connection to the internal server. It reports false
// when the server is shutting down.
func (s *Server) handoffConn(c net.Conn) bool {
	s.initHandoff()
	if !s.handoffLn.push(c) {
		c.Close()
		return false
	}
	return true
}

// handoffAddr is the fake address reported by the queue listener.
type handoffAddr struct{}

func (handoffAddr) Network() string { return "fastserve-handoff" }
func (handoffAddr) String() string  { return "fastserve-handoff" }

// Shutdown stops accepting (closing the listener Serve received, like
// http.Server), waits for connections that are serving a request up to ctx,
// closes idle connections, then shuts the handoff server. Hijacked
// connections (WebSockets) survive a graceful shutdown, exactly as they do
// under the replaced http.Server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.inShutdown {
		s.mu.Unlock()
		return nil
	}
	s.inShutdown = true
	ln := s.ln
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	// Wake idle connections out of their between-request wait; connections
	// mid-request finish naturally and stop at the next loop boundary.
	s.closeIdleConns()
	waited := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		s.closeConns()
		return ctx.Err()
	}
	if s.handoff != nil {
		if err := s.handoff.Shutdown(ctx); err != nil {
			return err
		}
	}
	return nil
}

// shuttingDown reports whether Shutdown or Close has started.
func (s *Server) shuttingDown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inShutdown
}

// closeIdleConns closes connections that are not mid-request.
func (s *Server) closeIdleConns() {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		if !c.busy.Load() {
			conns = append(conns, c)
		}
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.rwc.Close()
	}
}

// closeConns forces every tracked connection shut.
func (s *Server) closeConns() {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.rwc.Close()
	}
}

// Close stops the loop and closes every connection and the handoff server
// outright, mirroring http.Server.Close.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.inShutdown {
		s.mu.Unlock()
		s.closeConns()
		if s.handoffLn != nil {
			s.handoffLn.Close()
		}
		if s.handoff != nil {
			s.handoff.Close()
		}
		return nil
	}
	s.inShutdown = true
	ln := s.ln
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	s.closeConns()
	if s.handoffLn != nil {
		s.handoffLn.Close()
	}
	if s.handoff != nil {
		s.handoff.Close()
	}
	return nil
}
