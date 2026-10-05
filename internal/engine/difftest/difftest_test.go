package difftest

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func gzipMember(t *testing.T, plain string) []byte {
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

func TestExchangeReturnsBothSides(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Echo", r.Header.Get("X-Test"))
		io.WriteString(w, r.Method+" "+r.URL.RequestURI()+" "+string(payload))
	})
	engine, legacy, err := Run(handler, handler, Request{
		Method: "POST",
		Path:   "/rooms/1?before=5",
		Header: http.Header{"X-Test": {"value"}},
		Body:   []byte("payload"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if engine.Status != 200 || legacy.Status != 200 {
		t.Fatalf("statuses = %d, %d, want 200, 200", engine.Status, legacy.Status)
	}
	if !reflect.DeepEqual(engine.Header, legacy.Header) {
		t.Fatalf("headers differ\n engine: %v\n legacy: %v", engine.Header, legacy.Header)
	}
	if !bytes.Equal(engine.Body, legacy.Body) {
		t.Fatalf("bodies differ\n engine: %q\n legacy: %q", engine.Body, legacy.Body)
	}
	if got := string(engine.Body); got != "POST /rooms/1?before=5 payload" {
		t.Fatalf("body = %q", got)
	}
	if got := engine.Header.Get("X-Echo"); got != "value" {
		t.Fatalf("X-Echo = %q, want value", got)
	}
}

func TestExchangeSurfacesDifferences(t *testing.T) {
	one := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "one") })
	two := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "two") })
	engine, legacy, err := Run(one, two, Request{Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(engine.Body, legacy.Body) {
		t.Fatalf("bodies equal (%q); a mismatch must be visible", engine.Body)
	}
}

func TestDefaultMaskDropsVolatileHeaders(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", "Wed, 01 Oct 2026 00:00:00 GMT")
		w.Header().Set("X-Request-Start", "123456789")
		w.Header().Set("X-Keep", "yes")
		io.WriteString(w, "ok")
	})
	engine, legacy, err := Run(handler, handler, Request{Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]Result{"engine": engine, "legacy": legacy} {
		if got := result.Header.Get("Date"); got != "" {
			t.Fatalf("%s Date not masked: %q", name, got)
		}
		if got := result.Header.Get("X-Request-Start"); got != "" {
			t.Fatalf("%s X-Request-Start not masked: %q", name, got)
		}
		if got := result.Header.Get("X-Keep"); got != "yes" {
			t.Fatalf("%s X-Keep = %q, want yes", name, got)
		}
	}
}

func TestGzipBodyArrivesRaw(t *testing.T) {
	member := gzipMember(t, "compressed payload")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Write(member)
	})
	engine, legacy, err := Run(handler, handler, Request{Path: "/", Header: http.Header{"Accept-Encoding": {"gzip"}}})
	if err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]Result{"engine": engine, "legacy": legacy} {
		if got := result.Header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("%s Content-Encoding = %q, want gzip", name, got)
		}
		if !bytes.Equal(result.Body, member) {
			t.Fatalf("%s body re-encoded: %d bytes in, %d out", name, len(member), len(result.Body))
		}
	}
}

func TestHostIsForwarded(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Host)
	})
	engine, legacy, err := Run(handler, handler, Request{
		Path:   "/",
		Host:   "campfire.example.test",
		Header: http.Header{"Host": {"ignored.example.test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]Result{"engine": engine, "legacy": legacy} {
		if got := string(result.Body); got != "campfire.example.test" {
			t.Fatalf("%s host = %q, want campfire.example.test (Header[\"Host\"] must be ignored)", name, got)
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	engine, legacy, err := Run(handler, handler, Request{Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]Result{"engine": engine, "legacy": legacy} {
		if result.Status != http.StatusFound {
			t.Fatalf("%s status = %d, want 302", name, result.Status)
		}
		if got := result.Header.Get("Location"); got != "/elsewhere" {
			t.Fatalf("%s Location = %q, want /elsewhere", name, got)
		}
	}
}

func TestPairMaskIsExtensible(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Volatile", "per-exchange")
		w.Header().Set("X-Keep", "yes")
		io.WriteString(w, "ok")
	})
	pair := New(handler, handler)
	defer pair.Close()
	pair.Mask = append(pair.Mask, "X-Volatile")

	engine, legacy, err := pair.Exchange(Request{Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]Result{"engine": engine, "legacy": legacy} {
		if got := result.Header.Get("X-Volatile"); got != "" {
			t.Fatalf("%s X-Volatile not masked: %q", name, got)
		}
		if got := result.Header.Get("X-Keep"); got != "yes" {
			t.Fatalf("%s X-Keep = %q, want yes", name, got)
		}
	}
}
