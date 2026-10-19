package front

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// gzipPayload returns plain compressed as a fixed precomposed body to compare
// pass-through against byte for byte.
func gzipPayload(t *testing.T, plain string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(plain)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func gunzipPayload(t *testing.T, data []byte) string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
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
	return string(decoded)
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// TestServeSkipDeflate exercises encoding composition through Serve on the
// target listener, which bypasses the public (cache/compression) chain.
func TestServeSkipDeflate(t *testing.T) {
	preencoded := gzipPayload(t, "preencoded body")
	for _, skip := range []bool{false, true} {
		t.Run("SkipDeflate="+strconv.FormatBool(skip), func(t *testing.T) {
			config := FromLookup(func(string) (string, bool) { return "", false })
			config.HTTPPort = reservePort(t)
			config.TargetPort = reservePort(t)
			config.SkipDeflate = skip
			config.MaxRequestBody = 8
			config.LogRequests = false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- Serve(ctx, config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/plain":
						w.Header().Set("Content-Type", "text/plain")
						io.WriteString(w, "plain body")
					case "/preencoded":
						w.Header().Set("Content-Encoding", "gzip")
						w.Header().Set("Content-Type", "text/plain")
						w.Header().Set("X-Engine-Marker", "preserved")
						w.WriteHeader(201)
						w.Write(preencoded)
					default:
						http.NotFound(w, r)
					}
				}))
			}()
			client := &http.Client{Timeout: time.Second}
			defer client.CloseIdleConnections()
			target := "http://127.0.0.1:" + strconv.Itoa(config.TargetPort)
			get := func(path, acceptEncoding string) (*http.Response, []byte) {
				t.Helper()
				var response *http.Response
				var err error
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					request, requestErr := http.NewRequest("GET", target+path, nil)
					if requestErr != nil {
						t.Fatal(requestErr)
					}
					request.Header.Set("Accept-Encoding", acceptEncoding)
					response, err = client.Do(request)
					if err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				return response, body
			}

			plain, plainBody := get("/plain", "gzip")
			if skip {
				if got := plain.Header.Get("Content-Encoding"); got != "" {
					t.Fatalf("plain response encoded with %q despite SkipDeflate", got)
				}
				if string(plainBody) != "plain body" {
					t.Fatalf("plain body = %q", plainBody)
				}
			} else {
				if got := plain.Header.Get("Content-Encoding"); got != "gzip" {
					t.Fatalf("Content-Encoding = %q, want gzip", got)
				}
				if decoded := gunzipPayload(t, plainBody); decoded != "plain body" {
					t.Fatalf("gzip body decodes to %q, want a single layer", decoded)
				}
			}

			pre, preBody := get("/preencoded", "gzip")
			if pre.StatusCode != 201 {
				t.Fatalf("preencoded status = %d, want 201", pre.StatusCode)
			}
			if got := pre.Header.Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("preencoded Content-Encoding = %q, want gzip", got)
			}
			if got := pre.Header.Get("X-Engine-Marker"); got != "preserved" {
				t.Fatalf("preencoded marker = %q, want preserved", got)
			}
			if !bytes.Equal(preBody, preencoded) {
				t.Fatalf("preencoded body changed: %d bytes in, %d out", len(preencoded), len(preBody))
			}
			if decoded := gunzipPayload(t, preBody); decoded != "preencoded body" {
				t.Fatalf("preencoded body decodes to %q, want one gzip layer", decoded)
			}

			identity, identityBody := get("/plain", "identity")
			if got := identity.Header.Get("Content-Encoding"); got != "" {
				t.Fatalf("identity response encoded with %q", got)
			}
			if string(identityBody) != "plain body" {
				t.Fatalf("identity body = %q", identityBody)
			}

			// With SkipDeflate the caller owns negotiation: the front must not
			// reject an encoding set it cannot satisfy. Without it, Deflate
			// keeps its 406 policy.
			unacceptable, unacceptableBody := get("/plain", "gzip;q=0, identity;q=0")
			if skip {
				if unacceptable.StatusCode != 200 || string(unacceptableBody) != "plain body" {
					t.Fatalf("negotiation passthrough: status %d body %q", unacceptable.StatusCode, unacceptableBody)
				}
			} else if unacceptable.StatusCode != 406 {
				t.Fatalf("negotiation-failure status = %d, want 406", unacceptable.StatusCode)
			}

			limited, err := client.Post(target+"/plain", "text/plain", strings.NewReader("more than eight bytes"))
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, limited.Body)
			limited.Body.Close()
			if limited.StatusCode != 413 {
				t.Fatalf("oversized body status = %d, want 413", limited.StatusCode)
			}

			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(7 * time.Second):
				t.Fatal("shutdown hung")
			}
		})
	}
}

// TestServeSkipDeflatePublicListener runs a precomposed, cacheable response
// through the whole public chain (forward, cache, PublicCompression) to pin
// the takeover contract: the app's bytes, status and Vary survive untouched.
func TestServeSkipDeflatePublicListener(t *testing.T) {
	preencoded := gzipPayload(t, "public preencoded body")
	config := FromLookup(func(string) (string, bool) { return "", false })
	port := reservePort(t)
	config.HTTPPort = port
	config.TargetPort = port
	config.SkipDeflate = true
	config.LogRequests = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Vary", "Accept-Encoding")
			w.Header().Set("Cache-Control", "public, max-age=30")
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("X-Engine-Marker", "preserved")
			w.WriteHeader(201)
			w.Write(preencoded)
		}))
	}()
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/preencoded"
	var response *http.Response
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		request, requestErr := http.NewRequest("GET", url, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Accept-Encoding", "gzip")
		response, err = client.Do(request)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	check := func(wantCache string, response *http.Response) {
		t.Helper()
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 201 {
			t.Fatalf("public preencoded status = %d, want 201", response.StatusCode)
		}
		if got := response.Header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("public preencoded Content-Encoding = %q, want gzip", got)
		}
		if got := response.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
			t.Fatalf("public preencoded Vary = %q, want Accept-Encoding", got)
		}
		if got := response.Header.Get("X-Cache"); got != wantCache {
			t.Fatalf("public preencoded X-Cache = %q, want %q", got, wantCache)
		}
		if !bytes.Equal(body, preencoded) {
			t.Fatalf("public preencoded body changed: %d bytes in, %d out", len(preencoded), len(body))
		}
		if decoded := gunzipPayload(t, body); decoded != "public preencoded body" {
			t.Fatalf("public preencoded body decodes to %q, want one gzip layer", decoded)
		}
	}
	check("miss", response)

	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	check("hit", response)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("shutdown hung")
	}
}
