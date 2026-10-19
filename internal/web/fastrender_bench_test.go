package web

import (
	"bytes"
	"html/template"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// benchmarkView is a representative post-message fragment: rich text body,
// one boost, no attachment (the common case).
func benchmarkView(app *Server) messageView {
	now := time.Date(2026, 10, 6, 12, 30, 45, 123456000, time.UTC)
	return messageView{
		AllEmoji:     false,
		Message:      database.Message{ID: 933434569, RoomID: 486777696, CreatorID: 7, ClientID: "cl-8fj2k3", Body: "Hello <strong>@sebi</strong> 🚀", Creator: "Sebastian", CreatedAt: now, UpdatedAt: now},
		HTML:         template.HTML(`<p>Hello <strong>@sebi</strong>, this is a fairly long message body with some <a href="https://example.com/x?y=1&amp;z=2">link</a> and 🚀 emoji</p>`),
		Permalink:    "http://example.org/rooms/486777696/@933434569",
		CreatorTitle: "Sebastian",
		RoomName:     "All Talk",
		Boosts: []database.Boost{
			{BoosterTitle: "Max", BoosterUpdatedAt: now, ID: 41, MessageID: 933434569, BoosterID: 9, Content: "nice 🎉", Booster: "Max", CreatedAt: now, UpdatedAt: now},
		},
	}
}

func BenchmarkFastRender(b *testing.B) {
	app, _, _, _ := testApp(b)
	v := benchmarkView(app)
	if app.fastRender == nil {
		b.Fatal("fast renderer not compiled")
	}
	out := make([]byte, 0, 4096)
	out = app.fastRender.render(out, &v)
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out = app.fastRender.render(out[:0], &v)
	}
	benchmarkSink = out
}

func BenchmarkMarkupFragment(b *testing.B) {
	app, _, _, _ := testApp(b)
	v := benchmarkView(app)
	var buf bytes.Buffer
	if err := app.templates.ExecuteTemplate(&buf, "message-uncached", v); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(buf.Len()))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := app.templates.ExecuteTemplate(&buf, "message-uncached", v); err != nil {
			b.Fatal(err)
		}
	}
	benchmarkSink = buf.Bytes()
}

var benchmarkSink []byte

func BenchmarkFastRenderCore(b *testing.B) {
	// Same compiled program with the avatar signing cost stubbed out: the
	// difference to BenchmarkFastRender is exactly the inherited signing
	// allocation; this variant isolates the renderer's own allocation count.
	app, _, _, _ := testApp(b)
	v := benchmarkView(app)
	r := &messageRenderer{root: app.fastRender.root, avatar: func(int64, time.Time) string { return "/users/0/avatar" }}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkSink = r.render(benchmarkSink[:0], &v)
	}
}
