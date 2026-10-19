package web

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConditionalWeakComparisonStdlib(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		for _, etag := range []string{`W/"abc"`, `"abc"`, `W/"a,b"`, `W/""`} {
			for _, value := range []string{`"abc"`, `W/"abc"`, `"other"`, `"ABC"`, `*`, " \t*\t ", `"other", "abc"`, `W/"a,b"`, `"a,b"`, `""`, `, , "abc"`, `broken, "abc"`, `W/abc`, `"unterminated`, "\"a b\", \"abc\"", `w/"abc"`, `"other" "abc"`} {
				t.Run(method+"/"+etag+"/"+value, func(t *testing.T) {
					r := httptest.NewRequest(method, "/", nil)
					r.Header.Set("If-None-Match", value)
					standard := httptest.NewRecorder()
					standard.Header().Set("ETag", etag)
					http.ServeContent(standard, r, "test.txt", time.Time{}, strings.NewReader("body"))
					want := standard.Code == http.StatusNotModified
					if got := requestIsFresh(r, []byte(etag), time.Time{}); got != want {
						t.Errorf("framed fresh=%v; net/http=%v", got, want)
					}
					w := httptest.NewRecorder()
					if got := notModified(w, r, etag, time.Time{}); got != want {
						t.Errorf("map fresh=%v; net/http=%v", got, want)
					}
					if want && w.Header().Get("ETag") != standard.Header().Get("ETag") {
						t.Errorf("304 ETag=%q; net/http=%q", w.Header().Get("ETag"), standard.Header().Get("ETag"))
					}
				})
			}
		}
	}
}

func TestConditionalMatcherAllocations(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("If-None-Match", `"other", W/"a,b", "abc"`)
	etag := []byte(`W/"abc"`)
	var fresh bool
	allocs := testing.AllocsPerRun(100, func() {
		fresh = requestIsFresh(r, etag, time.Time{})
	})
	if !fresh || allocs != 0 {
		t.Fatalf("framed matcher fresh=%v allocations=%g", fresh, allocs)
	}
	// An unmatched map request measures parsing without header-map emission.
	w := httptest.NewRecorder()
	allocs = testing.AllocsPerRun(100, func() {
		fresh = notModified(w, r, `W/"different"`, time.Time{})
	})
	if fresh || allocs != 0 {
		t.Fatalf("map matcher fresh=%v allocations=%g", fresh, allocs)
	}
}

// Optional external evidence is read-only. Replay the original request bytes
// at the conditional-response boundary using the original 200 representation.
func TestConditionalConformanceProbes(t *testing.T) {
	dir := os.Getenv("CAMPFIRE_CONFORMANCE_PROBES")
	if dir == "" {
		t.Skip("set CAMPFIRE_CONFORMANCE_PROBES to replay external byte pairs")
	}
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	first, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(read("conditional-first.response"))), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(first.Body)
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatal("200 probe has no ETag")
	}
	for _, name := range []string{"conditional-strong-equivalent", "conditional-same", "conditional-head"} {
		t.Run(name, func(t *testing.T) {
			r, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(read(name + ".request"))))
			if err != nil {
				t.Fatal(err)
			}
			original, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(read(name+".response"))), r)
			if err != nil {
				t.Fatal(err)
			}
			originalBody, err := io.ReadAll(original.Body)
			if err != nil {
				t.Fatal(err)
			}
			original.Body.Close()
			t.Logf("original probe: status=%d body=%d ETag=%q", original.StatusCode, len(originalBody), original.Header.Get("ETag"))
			w := httptest.NewRecorder()
			if !notModified(w, r, etag, time.Time{}) {
				w.Header().Set("ETag", etag)
				if r.Method != "HEAD" {
					w.Write(body)
				}
			}
			t.Logf("boundary replay: status=%d body=%d ETag=%q", w.Code, w.Body.Len(), w.Header().Get("ETag"))
			if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("ETag") != etag {
				t.Errorf("want bodyless 304 with ETag %q", etag)
			}
		})
	}
}

func TestConditional304WireValidators(t *testing.T) {
	for _, chain := range replayChains() {
		t.Run(chain.name, func(t *testing.T) {
			on, off, cookie, user := testResponseCacheApps(t)
			roomID := seedReplayRoom(t, on, user)
			_, onAddr := serveLoop(t, chain.mk(on))
			_, offAddr := serveLoop(t, chain.mk(off))
			for _, shape := range replayShapes(roomID) {
				t.Run(shape.name, func(t *testing.T) {
					fill, status := rawFetch(t, onAddr, "campfire.test", "GET", shape.path, shape.accept, "", cookie, shape.extra...)
					etag := headerValue(fill, "Etag")
					if status != 200 || etag == "" {
						t.Fatalf("fill status=%d ETag=%q", status, etag)
					}
					for _, method := range []string{"GET", "HEAD"} {
						for _, value := range []string{etag, strings.TrimPrefix(etag, "W/"), `"different"`, `"other", ` + strings.TrimPrefix(etag, "W/"), "*"} {
							for _, lane := range []struct{ name, addr string }{{"fresh", offAddr}, {"replay", onAddr}} {
								t.Run(method+"/"+value+"/"+lane.name, func(t *testing.T) {
									wire, status := rawFetch(t, lane.addr, "campfire.test", method, shape.path, shape.accept, value, cookie, shape.extra...)
									want := 304
									if value == `"different"` {
										want = 200
									}
									if status != want || headerValue(wire, "Etag") != etag {
										t.Errorf("status=%d ETag=%q; want %d %q", status, headerValue(wire, "Etag"), want, etag)
									}
									if want == 304 && !bytes.HasSuffix(wire, []byte("\r\n\r\n")) {
										t.Error("304 has a body")
									}
								})
							}
						}
					}
				})
			}
		})
	}
}
