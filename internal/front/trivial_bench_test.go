package front

// BenchmarkTrivial establishes the measurement floor: one keep-alive GET
// against a handler that sets Content-Type, Content-Length and writes a
// fixed body — the least a real server can do. The delta between this and the
// chain benchmarks is the server-side work of the front chain.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func BenchmarkTrivialSmallBody(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(benchUpBody)))
		io.WriteString(w, benchUpBody)
	})
	server := httptest.NewServer(handler)
	b.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}, Timeout: 10 * time.Second}
	b.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequest("GET", server.URL+"/x", nil)
	if err != nil {
		b.Fatal(err)
	}
	for name, value := range map[string]string{"Accept-Encoding": "gzip", "Cookie": "_campfire_session=bench; session_token=bench-token-bench-token-ben"} {
		request.Header.Set(name, value)
	}
	if response, err := client.Do(request); err != nil {
		b.Fatal(err)
	} else {
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response, err := client.Do(request)
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
}

func BenchmarkTrivialMediumBody(b *testing.B) {
	body := make([]byte, 3364)
	for i := range body {
		body[i] = byte(i)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(200)
		w.Write(body)
	})
	server := httptest.NewServer(handler)
	b.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}, Timeout: 10 * time.Second}
	b.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequest("GET", server.URL+"/x", nil)
	if err != nil {
		b.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	if response, err := client.Do(request); err != nil {
		b.Fatal(err)
	} else {
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response, err := client.Do(request)
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
}

// BenchmarkTrivialMediumBodyChunked is the replay shape: no Content-Length,
// so net/http frames the 3364-byte body in chunks (the captured replay
// headers never carry a Content-Length, exactly like the application path
// that produced them).
func BenchmarkTrivialMediumBodyChunked(b *testing.B) {
	body := make([]byte, 3364)
	for i := range body {
		body[i] = byte(i)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		w.WriteHeader(200)
		w.Write(body)
	})
	server := httptest.NewServer(handler)
	b.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}, Timeout: 10 * time.Second}
	b.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequest("GET", server.URL+"/x", nil)
	if err != nil {
		b.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	if response, err := client.Do(request); err != nil {
		b.Fatal(err)
	} else {
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response, err := client.Do(request)
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
}
