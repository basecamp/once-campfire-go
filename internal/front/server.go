package front

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/fastserve"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

func (c Config) server(address string, handler http.Handler) *http.Server {
	headerTimeout := c.ReadTimeout
	if c.IdleTimeout > 0 && (headerTimeout == 0 || c.IdleTimeout < headerTimeout) {
		headerTimeout = c.IdleTimeout
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(c.H2C)
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: headerTimeout, ReadTimeout: c.ReadTimeout, WriteTimeout: c.WriteTimeout, IdleTimeout: c.IdleTimeout, MaxHeaderBytes: 64 << 10, Protocols: protocols}
}

// newLoop builds the owned server loop for one listener, mirroring the
// net/http server it replaces: the same handler, the same timing envelope, the
// same MaxHeaderBytes and, for the upgrade/h2c handoff server, the same
// protocol configuration.
func newLoop(server *http.Server, maxBody int64) *fastserve.Server {
	loop := fastserve.New(server.Handler)
	loop.ReadTimeout = server.ReadTimeout
	loop.ReadHeaderTimeout = server.ReadHeaderTimeout
	loop.WriteTimeout = server.WriteTimeout
	loop.IdleTimeout = server.IdleTimeout
	loop.MaxHeaderBytes = int64(server.MaxHeaderBytes)
	loop.MaxBodySize = maxBody
	loop.Protocols = server.Protocols
	return loop
}
func bodyLimit(next http.Handler, limit int64) http.Handler {
	if limit <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > limit {
			http.Error(w, "Request Entity Too Large", 413)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
func forward(next http.Handler, c Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server allocates a fresh Request and Header map per request
		// (net/http.readRequestLimit), so the header edits below are made in
		// place: cloning the request bought nothing and cost the largest
		// per-request allocation in the public chain (a full header-map copy
		// for the cookie-heavy loadgen requests).
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !c.ForwardHeaders {
			for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Port", "X-Forwarded-Proto", "Forwarded"} {
				r.Header.Del(name)
			}
		}
		if previous := r.Header.Get("X-Forwarded-For"); previous != "" {
			r.Header.Set("X-Forwarded-For", previous+", "+host)
		} else {
			r.Header.Set("X-Forwarded-For", host)
		}
		if r.Header.Get("X-Forwarded-Host") == "" {
			r.Header.Set("X-Forwarded-Host", r.Host)
		}
		if r.Header.Get("X-Forwarded-Proto") == "" {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			r.Header.Set("X-Forwarded-Proto", scheme)
		}
		r.Header.Set("X-Request-Start", strconv.FormatInt(time.Now().UnixMilli(), 10))
		started := time.Now()
		next.ServeHTTP(w, r)
		if c.LogRequests {
			slog.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
		}
	})
}

// Serve hosts the application listener and the public HTTP/TLS listeners in one
// process. The ACME manager is Go's autocert, also used by Thruster.
func Serve(ctx context.Context, c Config, app http.Handler) error {
	// SkipDeflate hands encoding to the app; see Config.SkipDeflate.
	// bodyLimit applies in both modes.
	if c.SkipDeflate {
		app = bodyLimit(app, c.MaxRequestBody)
	} else {
		app = bodyLimit(Deflate(app), c.MaxRequestBody)
	}
	cache := NewCache(c.CacheSize, c.MaxCacheItemSize)
	cache.FixedRoutes = c.FixedRoutes
	public := forward(PublicCompression(cache.Handler(app), c), c)
	var servers []*http.Server
	var listeners []net.Listener
	// loops holds the owned-loop servers replacing a net/http target
	// listener (CAMPFIRE_SERVER_LOOP=on); the same shutdown lifecycle runs
	// on them.
	var loops []*fastserve.Server
	add := func(server *http.Server) error {
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			return err
		}
		servers = append(servers, server)
		listeners = append(listeners, listener)
		loops = append(loops, nil) // index-aligned; the loop replaces this server
		return nil
	}
	defer func() {
		for _, listener := range listeners {
			listener.Close()
		}
	}()
	if c.TargetPort != c.HTTPPort && (len(c.Domains) == 0 || c.TargetPort != c.HTTPSPort) {
		target := c.server(net.JoinHostPort(c.TargetBind, strconv.Itoa(c.TargetPort)), app)
		target.Protocols.SetHTTP2(false)
		target.Protocols.SetUnencryptedHTTP2(false)
		if err := add(target); err != nil {
			return err
		}
		if c.ServerLoop {
			// The owned loop takes over the internal listener. It gets the
			// same timing envelope, the same handler, and the same protocol
			// configuration for its upgrade/h2c handoff server.
			loops[len(loops)-1] = newLoop(target, c.MaxRequestBody)
			slog.Info("internal listener on fastserve loop", "address", listeners[len(listeners)-1].Addr())
		}
	}
	if len(c.Domains) == 0 {
		publicServer := c.server(":"+strconv.Itoa(c.HTTPPort), public)
		if err := add(publicServer); err != nil {
			return err
		}
		if c.ServerLoop {
			// Plain HTTP public listener: the owned loop serves the public
			// chain (forward, PublicCompression, the response cache and the
			// precomposed replay lane) directly. Upgrade-headed connections
			// (WebCable) and h2c prefaces are handed off to the same net/http
			// handoff server the internal listener uses, so hijacking is
			// unchanged. TLS/ACME listeners below always stay on net/http.
			loops[len(loops)-1] = newLoop(publicServer, c.MaxRequestBody)
			slog.Info("public listener on fastserve loop", "address", listeners[len(listeners)-1].Addr())
		}
	} else {
		manager := &autocert.Manager{Prompt: autocert.AcceptTOS, Cache: autocert.DirCache(c.StoragePath), HostPolicy: autocert.HostWhitelist(c.Domains...), Client: &acme.Client{DirectoryURL: c.ACMEDirectory}}
		if c.EABKeyID != "" {
			key, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(c.EABKey, "="))
			if err != nil {
				return fmt.Errorf("invalid EAB_HMAC_KEY: %w", err)
			}
			manager.ExternalAccountBinding = &acme.ExternalAccountBinding{KID: c.EABKeyID, Key: key}
		}
		tlsServer := c.server(":"+strconv.Itoa(c.HTTPSPort), public)
		tlsServer.TLSConfig = manager.TLSConfig()
		tlsServer.TLSConfig.MinVersion = tls.VersionTLS12
		if err := add(tlsServer); err != nil {
			return err
		}
		redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if c.HTTPSPort != 443 {
				host = net.JoinHostPort(host, strconv.Itoa(c.HTTPSPort))
			}
			http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), 301)
		})
		if err := add(c.server(":"+strconv.Itoa(c.HTTPPort), manager.HTTPHandler(redirect))); err != nil {
			return err
		}
	}
	results := make(chan error, len(servers))
	for i, server := range servers {
		listener := listeners[i]
		loop := loops[i]
		slog.Info("listening", "address", listener.Addr(), "tls", server.TLSConfig != nil, "loop", loop != nil)
		go func() {
			if loop != nil {
				results <- loop.Serve(listener)
				return
			}
			if server.TLSConfig != nil {
				results <- server.ServeTLS(listener, "", "")
			} else {
				results <- server.Serve(listener)
			}
		}()
	}
	var result error
	select {
	case <-ctx.Done():
	case result = <-results:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped := make(chan error, len(servers))
	for i, server := range servers {
		go func() {
			if loop := loops[i]; loop != nil {
				stopped <- loop.Shutdown(shutdown)
				return
			}
			err := server.Shutdown(shutdown)
			if err != nil {
				server.Close()
			}
			stopped <- err
		}()
	}
	for range servers {
		result = errors.Join(result, <-stopped)
	}
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}
