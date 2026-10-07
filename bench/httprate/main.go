// httprate measures HTTP latency under a fixed offered request rate.
// Arrivals are scheduled independently of completions. Reported latency starts
// at the scheduled arrival, including generator lateness and client queueing.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/bits"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type config struct {
	Base, Cookie, Path, PostRoom, CSRF string
	Rate                               float64
	Duration, Timeout, Drain           time.Duration
	Concurrency, Queue                 int
	Gzip                               bool
}

// Microsecond buckets retain 10 significant binary digits. Quantiles report
// each bucket's upper bound; resolution is 1 us below 1 ms and <0.2% thereafter.
// Counts, mean and maximum are not quantized. Size is independent of run length.
type histogram struct {
	buckets [32768]uint64
	n       uint64
	sum     float64
	max     time.Duration
}

func (h *histogram) record(d time.Duration) {
	if d < 0 {
		d = 0
	}
	v := uint64(d.Microseconds())
	shift := max(0, bits.Len64(v)-10)
	h.buckets[(v>>shift)+uint64(shift*512)]++
	h.n++
	h.sum += float64(d)
	h.max = max(h.max, d)
}

func (h *histogram) add(other *histogram) {
	for i, n := range other.buckets {
		h.buckets[i] += n
	}
	h.n += other.n
	h.sum += other.sum
	h.max = max(h.max, other.max)
}

func (h *histogram) quantile(q float64) float64 {
	if h.n == 0 {
		return 0
	}
	target := uint64(math.Ceil(float64(h.n) * q))
	var count uint64
	for i, n := range h.buckets {
		count += n
		if count < target {
			continue
		}
		if i < 1024 {
			return float64(i+1) / 1000
		}
		shift := (i-1024)/512 + 1
		mantissa := 512 + (i-1024)%512
		return float64((uint64(mantissa)+1)<<shift) / 1000
	}
	panic("histogram count differs from buckets")
}

func (h *histogram) summary() map[string]any {
	if h.n == 0 {
		return map[string]any{"n": uint64(0)}
	}
	return map[string]any{
		"n": h.n, "p50_ms": h.quantile(.5), "p90_ms": h.quantile(.9),
		"p95_ms": h.quantile(.95), "p99_ms": h.quantile(.99),
		"max_ms":  float64(h.max) / float64(time.Millisecond),
		"mean_ms": h.sum / float64(h.n) / float64(time.Millisecond),
	}
}

type job struct {
	index               uint64
	scheduled, enqueued time.Time
}

type counters struct {
	attempted, ok, networkErrors, statusErrors, emptyBodies, expired uint64
	bytes                                                            uint64
	statuses                                                         map[int]uint64
	latency, service, queue                                          histogram
}

type result struct {
	Mode             string         `json:"mode"`
	Path             string         `json:"path"`
	Rate             float64        `json:"offered_rps"`
	Concurrency      int            `json:"conc"`
	QueueCapacity    int            `json:"queue_capacity"`
	QueuePeak        int            `json:"queue_peak"`
	Gzip             bool           `json:"gzip"`
	Seconds          float64        `json:"secs"`
	Elapsed          float64        `json:"elapsed_secs"`
	Drain            float64        `json:"drain_secs"`
	Planned          uint64         `json:"planned"`
	Scheduled        uint64         `json:"scheduled"`
	Attempted        uint64         `json:"attempted"`
	OK               uint64         `json:"ok"`
	Errors           uint64         `json:"errors"`
	NetworkErrors    uint64         `json:"network_errors"`
	StatusErrors     uint64         `json:"status_errors"`
	EmptyBodies      uint64         `json:"empty_bodies"`
	Dropped          uint64         `json:"dropped"`
	Expired          uint64         `json:"expired"`
	Unscheduled      uint64         `json:"unscheduled"`
	RPS              float64        `json:"rps"`
	AverageBytes     uint64         `json:"avg_bytes"`
	Statuses         map[int]uint64 `json:"statuses"`
	Latency          map[string]any `json:"latency"`
	Service          map[string]any `json:"service_latency"`
	Queue            map[string]any `json:"queue_latency"`
	Generator        map[string]any `json:"generator_lateness"`
	QuantileAccuracy string         `json:"quantile_accuracy"`
}

func validate(c config) error {
	u, err := url.Parse(c.Base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base must be an HTTP(S) URL without a query or fragment")
	}
	if !strings.HasPrefix(c.Path, "/") || c.Concurrency < 1 || c.Queue < 0 {
		return fmt.Errorf("path must begin with /, concurrency must be positive, queue must be nonnegative")
	}
	if c.Duration <= 0 || c.Timeout <= 0 || c.Drain <= 0 || c.Rate <= 0 || math.IsNaN(c.Rate) || math.IsInf(c.Rate, 0) {
		return fmt.Errorf("rate, duration, timeout and drain must be finite and positive")
	}
	if c.Duration > time.Duration(math.MaxInt64)-c.Drain || c.Rate*c.Duration.Seconds() >= float64(math.MaxUint64) {
		return fmt.Errorf("arrival count or total duration is too large")
	}
	return nil
}

func request(ctx context.Context, c config, j job, prefix string) (*http.Request, error) {
	method, path := http.MethodGet, c.Path
	var body io.Reader
	if c.PostRoom != "" {
		method, path = http.MethodPost, "/rooms/"+c.PostRoom+"/messages"
		body = strings.NewReader(url.Values{
			"message[body]":              {"bench write " + strconv.FormatUint(j.index, 10)},
			"message[client_message_id]": {prefix + strconv.FormatUint(j.index, 10)},
			"authenticity_token":         {c.CSRF},
		}.Encode())
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Base, "/")+path, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Cookie", c.Cookie)
	r.Header.Set("Accept-Encoding", "identity")
	if c.Gzip {
		r.Header.Set("Accept-Encoding", "gzip")
	}
	if c.PostRoom != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "text/vnd.turbo-stream.html, text/html, application/xhtml+xml")
		r.Header.Set("X-CSRF-Token", c.CSRF)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	return r, nil
}

func worker(ctx context.Context, c config, client *http.Client, jobs <-chan job, prefix string, s *counters) {
	s.statuses = make(map[int]uint64)
	for j := range jobs {
		started := time.Now()
		s.queue.record(started.Sub(j.enqueued))
		if ctx.Err() != nil {
			s.expired++
			continue
		}
		s.attempted++
		r, err := request(ctx, c, j, prefix)
		var response *http.Response
		if err == nil {
			response, err = client.Do(r)
		}
		if err != nil {
			s.networkErrors++
			continue
		}
		s.statuses[response.StatusCode]++
		n, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		s.bytes += uint64(n)
		if readErr != nil || closeErr != nil {
			s.networkErrors++
			continue
		}
		if response.StatusCode != http.StatusOK {
			s.statusErrors++
			continue
		}
		if n == 0 {
			s.emptyBodies++
			continue
		}
		finished := time.Now()
		s.ok++
		s.latency.record(finished.Sub(j.scheduled))
		s.service.record(finished.Sub(started))
	}
}

func run(parent context.Context, c config) (result, error) {
	if err := validate(c); err != nil {
		return result{}, err
	}
	transport := &http.Transport{
		Proxy:              http.ProxyFromEnvironment,
		DialContext:        (&net.Dialer{Timeout: c.Timeout, KeepAlive: 30 * time.Second}).DialContext,
		DisableCompression: true, MaxIdleConns: c.Concurrency, MaxIdleConnsPerHost: c.Concurrency,
		MaxConnsPerHost: c.Concurrency, IdleConnTimeout: 30 * time.Second,
		TLSHandshakeTimeout: c.Timeout, ForceAttemptHTTP2: false,
		TLSNextProto: make(map[string]func(string, *tls.Conn) http.RoundTripper),
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	jobs := make(chan job, c.Queue)
	stats := make([]counters, c.Concurrency)
	prefix := "rate-" + rand.Text() + "-"
	start := time.Now()
	ctx, cancel := context.WithDeadline(parent, start.Add(c.Duration+c.Drain))
	defer cancel()
	var wg sync.WaitGroup
	for i := range stats {
		wg.Go(func() { worker(ctx, c, client, jobs, prefix, &stats[i]) })
	}
	planned := uint64(math.Ceil(c.Rate * c.Duration.Seconds()))
	var scheduled, dropped uint64
	var peak int
	var lateness histogram
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for i := uint64(0); i < planned; i++ {
		deadline := start.Add(time.Duration(float64(i) / c.Rate * float64(time.Second)))
		if wait := time.Until(deadline); wait > 0 {
			timer.Reset(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			break
		}
		now := time.Now()
		lateness.record(now.Sub(deadline))
		scheduled++
		select {
		case jobs <- job{index: i, scheduled: deadline, enqueued: now}:
			peak = max(peak, len(jobs))
		default:
			dropped++
		}
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	var all counters
	all.statuses = make(map[int]uint64)
	for i := range stats {
		s := &stats[i]
		all.attempted += s.attempted
		all.ok += s.ok
		all.networkErrors += s.networkErrors
		all.statusErrors += s.statusErrors
		all.emptyBodies += s.emptyBodies
		all.expired += s.expired
		all.bytes += s.bytes
		all.latency.add(&s.latency)
		all.service.add(&s.service)
		all.queue.add(&s.queue)
		for status, n := range s.statuses {
			all.statuses[status] += n
		}
	}
	errors := all.networkErrors + all.statusErrors + all.emptyBodies + all.expired + dropped
	if all.ok+errors != scheduled {
		return result{}, fmt.Errorf("arrival accounting mismatch: %d scheduled, %d successful, %d errors", scheduled, all.ok, errors)
	}
	path := c.Path
	if c.PostRoom != "" {
		path = "POST /rooms/" + c.PostRoom + "/messages"
	}
	return result{
		Mode: "fixed-offered-rate", Path: path, Rate: c.Rate, Concurrency: c.Concurrency,
		QueueCapacity: c.Queue, QueuePeak: peak, Gzip: c.Gzip, Seconds: c.Duration.Seconds(),
		Elapsed: elapsed, Drain: c.Drain.Seconds(), Planned: planned, Scheduled: scheduled,
		Attempted: all.attempted, OK: all.ok, Errors: errors, NetworkErrors: all.networkErrors,
		StatusErrors: all.statusErrors, EmptyBodies: all.emptyBodies, Dropped: dropped,
		Expired: all.expired, Unscheduled: planned - scheduled,
		RPS:          float64(all.ok) / max(c.Duration.Seconds(), elapsed),
		AverageBytes: all.bytes / max(uint64(1), all.attempted), Statuses: all.statuses,
		Latency: all.latency.summary(), Service: all.service.summary(), Queue: all.queue.summary(), Generator: lateness.summary(),
		QuantileAccuracy: "upper bucket bound; 1 us below 1 ms, less than 0.2% thereafter; successful responses only",
	}, nil
}

func main() {
	var c config
	var duration, timeout, drain float64
	var gzip int
	flag.StringVar(&c.Base, "base", "", "HTTP(S) listener origin")
	flag.StringVar(&c.Cookie, "cookie", "", "authenticated cookie")
	flag.StringVar(&c.Path, "path", "/", "GET path")
	flag.StringVar(&c.PostRoom, "post-room", "", "POST messages to this room instead of GET")
	flag.StringVar(&c.CSRF, "csrf", "", "CSRF token for POST")
	flag.Float64Var(&c.Rate, "rate", 1000, "offered arrivals per second, independent of completions")
	flag.Float64Var(&duration, "duration", 10, "arrival window in seconds")
	flag.Float64Var(&timeout, "timeout", 10, "per-request timeout in seconds")
	flag.Float64Var(&drain, "drain", 10, "maximum completion grace after the arrival window, in seconds")
	flag.IntVar(&c.Concurrency, "conc", 16, "maximum simultaneous HTTP/1.1 requests")
	flag.IntVar(&c.Queue, "queue", 8192, "maximum pending arrivals; overflow is reported as dropped")
	flag.IntVar(&gzip, "gzip", 1, "request gzip (1) or identity (0); bodies are fully consumed as encoded")
	flag.Parse()
	for _, v := range []float64{duration, timeout, drain} {
		if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) || v >= float64(math.MaxInt64)/float64(time.Second) {
			fmt.Fprintln(os.Stderr, "duration, timeout and drain must be finite positive representable seconds")
			os.Exit(2)
		}
	}
	if flag.NArg() != 0 || (gzip != 0 && gzip != 1) {
		fmt.Fprintln(os.Stderr, "unexpected arguments or invalid gzip value")
		os.Exit(2)
	}
	c.Duration, c.Timeout, c.Drain, c.Gzip = time.Duration(duration*float64(time.Second)), time.Duration(timeout*float64(time.Second)), time.Duration(drain*float64(time.Second)), gzip == 1
	value, err := run(context.Background(), c)
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(value)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
