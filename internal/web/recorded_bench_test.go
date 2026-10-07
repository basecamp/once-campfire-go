package web

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/piececache"
)

// The recorded-response benchmarks model the M0 room page: ~374 KB of body,
// ~352 KB of it the message list, ~16 KB shell around the loadedAt and
// message-list insertion points.
const (
	benchLoadedMarker  = "campfire-loaded-benchmark"
	benchMessageMarker = "\x00campfire-benchmark\x00"
)

type benchResponseWriter struct{ header http.Header }

func (w *benchResponseWriter) Header() http.Header         { return w.header }
func (w *benchResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *benchResponseWriter) WriteHeader(int)             {}

type recordedBenchFixture struct {
	server   *Server
	page     page
	payload  *piececache.Entry
	shell    string
	fragment fragmentEntry
}

func benchmarkMarkup(size int, seed string) []byte {
	block := "<div class=\"message message--formatted\" data-controller=\"reply\"><div class=\"message__body\"><div class=\"message__body-content\"><p>" + seed + "</p></div></div></div>\n"
	var b strings.Builder
	b.Grow(size + len(block))
	for b.Len() < size {
		b.WriteString(block)
	}
	return []byte(b.String())
}

func newRecordedBenchmarkFixture(b *testing.B) *recordedBenchFixture {
	b.Helper()
	prefix := benchmarkMarkup(8<<10, "shell prefix")
	middle := benchmarkMarkup(8<<10, "shell middle")
	suffix := benchmarkMarkup(6<<10, "shell suffix")
	payloadRaw := benchmarkMarkup(352<<10, "message body")

	server := &Server{pieces: piececache.New(64 << 20), recordedPieces: true}
	fixture := &recordedBenchFixture{server: server, page: page{LoadedAt: "1767225845000"}}
	identity := recordedShellIdentity("room", fixture.page)
	manifest := make([]byte, 2+32*3)
	manifest[0], manifest[1] = 3, 1
	for i, segment := range [][]byte{prefix, middle, suffix} {
		key := shellSegmentKey(identity, i)
		copy(manifest[2+32*i:], key[:])
		if _, ok := server.pieces.PutDigest(key, segment, compressFragment(segment)); !ok {
			b.Fatalf("segment %d rejected", i)
		}
	}
	if _, ok := server.pieces.PutDigest(identity, manifest, nil); !ok {
		b.Fatal("manifest rejected")
	}
	payload, ok := server.pieces.PutDigest([32]byte{0x42}, payloadRaw, compressFragment(payloadRaw))
	if !ok {
		b.Fatal("payload rejected")
	}
	fixture.payload = payload
	fixture.shell = string(prefix) + benchLoadedMarker + string(middle) + benchMessageMarker + string(suffix)
	fixture.fragment = fragmentEntry{html: template.HTML(payloadRaw), digest: sha256.Sum256(payloadRaw), payload: payloadRaw}
	return fixture
}

func BenchmarkRecordedResponse(b *testing.B) {
	fixture := newRecordedBenchmarkFixture(b)
	writer := &benchResponseWriter{header: http.Header{}}
	request := httptest.NewRequest("GET", "/rooms/1", nil)
	gzipRequest := httptest.NewRequest("GET", "/rooms/1", nil)
	gzipRequest.Header.Set("Accept-Encoding", "gzip")
	compressor := gzip.NewWriter(io.Discard)

	b.Run("legacy-identity", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			clear(writer.header)
			shell := strings.ReplaceAll(fixture.shell, benchLoadedMarker, fixture.page.LoadedAt)
			buffered := &responseBuffer{ResponseWriter: writer}
			writeRecorded(buffered, 200, shell, benchMessageMarker, fixture.fragment)
			buffered.finish(request)
		}
	})
	b.Run("legacy-gzip", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			clear(writer.header)
			shell := strings.ReplaceAll(fixture.shell, benchLoadedMarker, fixture.page.LoadedAt)
			buffered := &responseBuffer{ResponseWriter: writer}
			writeRecorded(buffered, 200, shell, benchMessageMarker, fixture.fragment)
			// front.Deflate compresses the three parts as one gzip stream.
			compressor.Reset(io.Discard)
			for _, part := range buffered.parts {
				compressor.Write(part)
			}
			compressor.Close()
		}
	})
	b.Run("pieces-identity", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			clear(writer.header)
			buffered := &responseBuffer{ResponseWriter: writer}
			if _, err := fixture.server.writeRecordedPieces(buffered, request, 200, "room", fixture.page, recordedPayload{piece: fixture.payload}, "identity"); err != nil {
				b.Fatal(err)
			}
			buffered.finish(request)
		}
	})
	b.Run("pieces-gzip", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			clear(writer.header)
			buffered := &responseBuffer{ResponseWriter: writer}
			if _, err := fixture.server.writeRecordedPieces(buffered, gzipRequest, 200, "room", fixture.page, recordedPayload{piece: fixture.payload}, "gzip"); err != nil {
				b.Fatal(err)
			}
			buffered.finish(gzipRequest)
		}
	})
}

// benchPrecomposedWriter models the fastserve receiver for the framed bench:
// it accepts the precomposed emission and allocates nothing per call. The
// real receiver (fastserve.response.WritePrecomposed) is exercised byte-for-
// byte by the parity tests; this stub measures the web-side cost of the same
// contract.
type benchPrecomposedWriter struct {
	header http.Header
	head   []byte
	parts  [][]byte
	status int
}

func (w *benchPrecomposedWriter) Header() http.Header { return w.header }
func (w *benchPrecomposedWriter) Write([]byte) (int, error) {
	panic("framed writer must not receive raw writes")
}
func (w *benchPrecomposedWriter) WriteHeader(int) {}
func (w *benchPrecomposedWriter) WritePrecomposed(status int, head []byte, parts [][]byte) error {
	w.status, w.head, w.parts = status, head, parts
	return nil
}

// BenchmarkRecordedFramedHit runs the ENGINE-48/49 hit path end to end: the
// arena-backed response buffer, the variant-cached ETag, the precomposed head
// block and (for gzip) the arena-assembled body, emitted through the owned
// writer contract. The target is 0 allocs/op for both gzip and identity on
// the warm path; the first two iterations warm the variant cache and the
// arena block before the timer starts.
func BenchmarkRecordedFramedHit(b *testing.B) {
	fixture := newRecordedBenchmarkFixture(b)
	server := fixture.server
	server.precomposed = true
	writer := &benchPrecomposedWriter{header: http.Header{}}
	request := httptest.NewRequest("GET", "/rooms/1", nil)
	gzipRequest := httptest.NewRequest("GET", "/rooms/1", nil)
	gzipRequest.Header.Set("Accept-Encoding", "gzip")
	run := func(b *testing.B, req *http.Request) {
		clear(writer.header)
		// Warm: variant fill, pooled blocks warmed.
		arena := borrowRequestArena()
		buffered := borrowResponseBuffer(writer, arena)
		buffered.framed = true // ServeHTTP computes this from the writer walk
		if _, err := server.writeRecordedPieces(buffered, req, 200, "room", fixture.page, recordedPayload{piece: fixture.payload}, clientContentEncoding(req, false)); err != nil {
			b.Fatal(err)
		}
		buffered.finish(req)
		releaseResponseBuffer(buffered)
		releaseRequestArena(arena)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			clear(writer.header)
			arena := borrowRequestArena()
			buffered := borrowResponseBuffer(writer, arena)
			buffered.framed = true
			if _, err := server.writeRecordedPieces(buffered, req, 200, "room", fixture.page, recordedPayload{piece: fixture.payload}, clientContentEncoding(req, false)); err != nil {
				b.Fatal(err)
			}
			buffered.finish(req)
			releaseResponseBuffer(buffered)
			releaseRequestArena(arena)
		}
	}
	b.Run("identity", func(b *testing.B) { run(b, request) })
	b.Run("gzip", func(b *testing.B) { run(b, gzipRequest) })
}

// benchSentence builds deterministic, varied HTML text so the compressed
// payload has production-like entropy rather than one repeated fragment.
func benchSentence(seed int) string {
	words := []string{"campfire", "message", "thread", "weekly", "update", "review", "deploy", "server", "latency", "cache", "render", "template", "attachment", "meeting", "notes", "question", "answer", "timeline", "sidebar", "socket"}
	var b strings.Builder
	for i := 0; i < 24; i++ {
		b.WriteString(words[(seed+i*7)%len(words)])
		if i%6 == 5 {
			b.WriteString(". ")
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// BenchmarkRecordedHitPath runs the integrated per-request hit path:
// recordedMessageList (content key + payload lookup) -> writeRecordedPieces
// (shell lookup, validator, gzip assembly or identity parts) ->
// responseBuffer.finish. The payload is rendered once from 40 real messages
// through the template stack, so its size and entropy are production-like.
func BenchmarkRecordedHitPath(b *testing.B) {
	b.Setenv("CAMPFIRE_RECORDED_PIECES", "on")
	app, _, _, user := testApp(b)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		b.Fatal(err)
	}
	room := rooms[0]
	for i := 0; i < 40; i++ {
		body := fmt.Sprintf("<p>Message %d: <strong>%s</strong></p><ul><li>%s</li><li>%s</li></ul>", i, benchSentence(i), benchSentence(i+3), benchSentence(i+11))
		if _, err := app.DB.CreateMessage(ctx, user.ID, room.ID, fmt.Sprintf("hit-bench-%d", i), body, benchSentence(i)); err != nil {
			b.Fatal(err)
		}
	}
	messages, err := app.DB.MessagePageReferences(ctx, room.ID, 0, "around")
	if err != nil {
		b.Fatal(err)
	}
	if len(messages) != 40 {
		b.Fatalf("page has %d messages, want 40", len(messages))
	}
	raw := make([]database.Message, len(messages))
	copy(raw, messages)
	p := page{Room: room, User: user, Screen: "room", Chat: true, Title: room.Name, LoadedAt: "1767225845000"}
	payload, err := app.recordedMessageList(ctx, raw, true, false)
	if err != nil {
		b.Fatal(err)
	}
	if payload.piece == nil {
		b.Fatal("piece path disabled")
	}
	shell, err := app.shellPieces(p, "room", true, false)
	if err != nil {
		b.Fatal(err)
	}
	pageBytes := len(payload.piece.Raw) + len(p.LoadedAt)
	for i := 0; i < shell.count; i++ {
		pageBytes += len(shell.segments[i].Raw)
	}
	writer := &benchResponseWriter{header: http.Header{}}
	request := httptest.NewRequest("GET", "/rooms/1", nil)
	gzipRequest := httptest.NewRequest("GET", "/rooms/1", nil)
	gzipRequest.Header.Set("Accept-Encoding", "gzip")
	run := func(b *testing.B, req *http.Request, encoding string) {
		b.ReportAllocs()
		b.ResetTimer()
		b.ReportMetric(float64(pageBytes), "body_B")
		for i := 0; i < b.N; i++ {
			clear(writer.header)
			payload, err := app.recordedMessageList(ctx, raw, encoding == "gzip", app.zstdPieces && encoding == "zstd")
			if err != nil {
				b.Fatal(err)
			}
			buffered := &responseBuffer{ResponseWriter: writer}
			if _, err := app.writeRecordedPieces(buffered, req, 200, "room", p, payload, encoding); err != nil {
				b.Fatal(err)
			}
			buffered.finish(req)
		}
	}
	b.Run("gzip", func(b *testing.B) { run(b, gzipRequest, "gzip") })
	b.Run("identity", func(b *testing.B) { run(b, request, "identity") })
}
