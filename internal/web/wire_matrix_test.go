package web

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Date is the only masked field. The frozen clock stabilizes loadedAt;
// compression bytes, header order, validators and framing must match exactly.
// Each side has independent caches, so cold HEAD and subsequent replay are
// compared at the same cache state rather than warming one side with the other.
func TestRecordedPageWireMatrix(t *testing.T) {
	for _, cacheBytes := range []int{0, 8 << 20} {
		t.Run(fmt.Sprintf("response-cache=%d", cacheBytes), func(t *testing.T) {
			fixture := chainFixtureFor(t, "on")
			fixture.on.responses = newResponseCache(cacheBytes)
			fixture.off.responses = newResponseCache(cacheBytes)
			owned := servePublicLoop(t, composePublic(fixture.on, int64(cacheBytes)))
			standard := httptest.NewServer(composePublic(fixture.off, int64(cacheBytes)))
			defer standard.Close()
			oracle := strings.TrimPrefix(standard.URL, "http://")
			for _, path := range []string{fmt.Sprintf("/rooms/%d", fixture.room.ID), fmt.Sprintf("/rooms/%d/messages", fixture.room.ID), "/searches?q=parity", "/users/me/sidebar"} {
				for _, accept := range []string{"identity", "gzip", "zstd, gzip;q=0.5", "zstd, gzip;q=0, identity;q=0.5"} {
					for _, origin := range []struct{ host, scheme string }{{"campfire.test", "http"}, {"alternate.test:8443", "https"}, {"campfire.test", "http"}} {
						t.Run(path+"/"+accept+"/"+origin.host+"/"+origin.scheme, func(t *testing.T) {
							etag := ""
							for _, step := range []struct {
								method      string
								conditional bool
							}{{"HEAD", false}, {"GET", false}, {"GET", false}, {"HEAD", false}, {"GET", true}, {"HEAD", true}} {
								condition := ""
								if step.conditional {
									if etag == "" {
										t.Fatal("missing validator")
									}
									condition = "If-None-Match: " + etag + "\r\n"
								}
								request := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\nX-Forwarded-Proto: %s\r\nAccept-Encoding: %s\r\nCookie: session_token=%s; last_room=%d\r\nConnection: close\r\n%s\r\n", step.method, path, origin.host, origin.scheme, accept, fixture.cookie.Value, fixture.room.ID, condition)
								got, want := auditWire(t, owned, request), auditWire(t, oracle, request)
								if !bytes.Equal(got, want) {
									t.Fatalf("%s conditional=%t owned=%q standard=%q", step.method, step.conditional, got, want)
								}
								response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(got)), &http.Request{Method: step.method})
								if err != nil {
									t.Fatal(err)
								}
								response.Body.Close()
								status := 200
								if step.conditional {
									status = 304
								}
								if response.StatusCode != status {
									t.Fatalf("%s status=%d want=%d", step.method, response.StatusCode, status)
								}
								if !step.conditional {
									etag = response.Header.Get("ETag")
								}
							}
						})
					}
				}
			}
		})
	}
}
