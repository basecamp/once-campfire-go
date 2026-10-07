package main

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(base string) config {
	return config{Base: base, Path: "/", Rate: 100, Duration: 50 * time.Millisecond, Timeout: time.Second, Drain: time.Second, Concurrency: 1, Queue: 16}
}

func TestScheduledLatencyIncludesBacklog(t *testing.T) {
	var first sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Do(func() { time.Sleep(100 * time.Millisecond) })
		io.WriteString(w, "complete")
	}))
	defer server.Close()
	value, err := run(context.Background(), testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if value.Planned != 5 || value.Scheduled != 5 || value.OK != 5 || value.Errors != 0 {
		t.Fatalf("lost arrivals under backlog: %+v", value)
	}
	if value.Latency["p50_ms"].(float64) < 40 {
		t.Fatalf("scheduled latency omitted backlog: %v", value.Latency)
	}
	if value.Queue["p50_ms"].(float64) < 40 {
		t.Fatalf("queue delay omitted backlog: %v", value.Queue)
	}
}

func TestFullBodiesAndFailedResponses(t *testing.T) {
	for _, mode := range []string{"complete", "redirect", "truncated", "empty"} {
		t.Run(mode, func(t *testing.T) {
			var redirects atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/followed" {
					redirects.Add(1)
				}
				switch mode {
				case "complete":
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					io.WriteString(w, strings.Repeat("x", 10000))
				case "redirect":
					http.Redirect(w, r, "/followed", 302)
				case "truncated":
					w.Header().Set("Content-Length", "100")
					io.WriteString(w, "short")
				case "empty":
					w.WriteHeader(200)
				}
			}))
			defer server.Close()
			c := testConfig(server.URL)
			c.Rate = 1 // Exactly one arrival; no retries or redirected requests.
			value, err := run(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if value.Attempted != 1 || value.OK+value.Errors != 1 || redirects.Load() != 0 {
				t.Fatalf("bad arrival/redirect accounting: %+v", value)
			}
			if mode == "complete" {
				if value.OK != 1 || value.AverageBytes != 10000 {
					t.Fatalf("did not consume complete body: %+v", value)
				}
			} else if value.Errors != 1 || value.Latency["n"] != uint64(0) {
				t.Fatalf("failed response counted as successful latency: %+v", value)
			}
		})
	}
}

func TestOverloadAndCancellationAreAccounted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	c := testConfig(server.URL)
	c.Queue, c.Drain = 1, 20*time.Millisecond
	value, err := run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if value.OK != 0 || value.Scheduled != 5 || value.Errors != 5 || value.Dropped == 0 || value.NetworkErrors == 0 || value.Expired == 0 {
		t.Fatalf("overload was hidden or queued work escaped cancellation: %+v", value)
	}
}

func TestPostRequestContract(t *testing.T) {
	c := testConfig("http://example.test")
	c.Cookie, c.PostRoom, c.CSRF, c.Gzip = "session=fixture", "42", "csrf", true
	r, err := request(context.Background(), c, job{index: 7}, "unique-")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "POST" || r.URL.String() != "http://example.test/rooms/42/messages" ||
		r.Header.Get("Cookie") != c.Cookie || r.Header.Get("Accept-Encoding") != "gzip" ||
		r.Header.Get("Sec-Fetch-Site") != "same-origin" || r.Header.Get("X-CSRF-Token") != c.CSRF ||
		values.Get("message[body]") != "bench write 7" || values.Get("message[client_message_id]") != "unique-7" || values.Get("authenticity_token") != c.CSRF {
		t.Fatalf("POST no longer matches the reference load generator: %v %v %v", r.Method, r.Header, values)
	}
}

func TestQuantileBoundsAndMerge(t *testing.T) {
	var merged histogram
	for _, d := range []time.Duration{0, 511 * time.Microsecond, 1023 * time.Microsecond, 1024 * time.Microsecond, 2048 * time.Microsecond, time.Second, time.Duration(math.MaxInt64)} {
		var h histogram
		h.record(d)
		upper := h.quantile(1) * float64(time.Millisecond)
		if upper < float64(d) || upper-float64(d) > max(float64(time.Microsecond), float64(d)*.002) {
			t.Fatalf("incorrect quantile bound for %v: %v ns", d, upper)
		}
		merged.add(&h)
	}
	if merged.summary()["n"] != uint64(7) || merged.quantile(.5) != 1.026 || merged.max != time.Duration(math.MaxInt64) {
		t.Fatalf("lost histogram values: %v", merged.summary())
	}
}
