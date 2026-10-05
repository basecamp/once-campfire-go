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
		r = r.Clone(r.Context())
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
	public := forward(PublicCompression(NewCache(c.CacheSize, c.MaxCacheItemSize).Handler(app), c), c)
	var servers []*http.Server
	var listeners []net.Listener
	add := func(server *http.Server) error {
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			return err
		}
		servers = append(servers, server)
		listeners = append(listeners, listener)
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
	}
	if len(c.Domains) == 0 {
		if err := add(c.server(":"+strconv.Itoa(c.HTTPPort), public)); err != nil {
			return err
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
		slog.Info("listening", "address", listener.Addr(), "tls", server.TLSConfig != nil)
		go func() {
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
	for _, server := range servers {
		go func() {
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
