package web

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
)

func BenchmarkReviewAdmission(b *testing.B) {
	app, _, _, _, _ := benchReplayFixture(b)
	for _, size := range []int{32 << 10, 384 << 10} {
		for _, outcome := range []string{"accepted", "oversized", "generation-rejected", "encoded-accepted"} {
			b.Run(fmt.Sprintf("%d/%s", size, outcome), func(b *testing.B) {
				v, err := app.DB.ResponseVersion(context.Background())
				if err != nil {
					b.Fatal(err)
				}
				if outcome == "generation-rejected" {
					v--
				}
				app.responses = newResponseCache(64 << 20)
				app.responses.version = v
				if outcome == "oversized" {
					app.responses.limit = size
				}
				info := &requestInfo{host: "campfire.test", response: &responseRound{version: v, user: 1, keyed: true}}
				r := httptest.NewRequest("GET", "/rooms/1/messages", nil)
				r = r.WithContext(context.WithValue(r.Context(), requestInfoKey{}, info))
				w := borrowResponseBuffer(httptest.NewRecorder(), nil)
				defer releaseResponseBuffer(w)
				w.status = 200
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("ETag", `W/"test"`)
				w.parts = [][]byte{make([]byte, size/2), make([]byte, size-size/2)}
				if outcome == "encoded-accepted" {
					info.response.gzip = true
					w.parts = nil
					w.encoded = make([]byte, size)
					w.wireEncoding = "gzip"
					w.Header().Set("Content-Encoding", "gzip")
				}
				app.cacheResponse(r, w)
				accepted := outcome == "accepted" || outcome == "encoded-accepted"
				if accepted && len(app.responses.entries) != 1 {
					b.Fatal("admission did not store")
				}
				if !accepted && len(app.responses.entries) != 0 {
					b.Fatal("rejected admission stored")
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					app.cacheResponse(r, w)
				}
			})
		}
	}
}
