package web

import (
	"bytes"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func BenchmarkMessageUncached(b *testing.B) {
	app, _, _, user := testApp(b)
	now := time.Date(2026, 3, 2, 16, 0, 0, 0, time.UTC)
	view := messageView{Message: database.Message{ID: 933434529, RoomID: 486777696, CreatorID: user.ID, ClientID: "6434095e-cc52-50de-99e7-e3878818b363", Creator: user.Name, CreatedAt: now, UpdatedAt: now}, HTML: "<div class=\"lexxy-content\"><p>Hello</p></div>", Permalink: "http://example.test/rooms/486777696/@933434529", CreatorTitle: user.Name, CreatorUpdatedAt: now, RoomName: "Watercooler"}
	b.Run("template", func(b *testing.B) {
		var out bytes.Buffer
		for b.Loop() {
			out.Reset()
			if err := app.templates.ExecuteTemplate(&out, "message-uncached", view); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("renderer", func(b *testing.B) {
		for b.Loop() {
			app.renderer.render(view)
		}
	})
}
