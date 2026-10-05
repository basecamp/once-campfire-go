package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func TestMessageMarkupMatchesTemplate(t *testing.T) {
	app, _, _, _ := testApp(t)
	if len(app.messageTemplates[0]) == 0 {
		t.Fatal("message template changed: update slot renderer and byte comparison cases")
	}
	base := messageView{Message: database.Message{ID: 123, RoomID: 456, CreatorID: 789, ClientID: "client-id", Creator: "A & B", CreatedAt: time.Now(), UpdatedAt: time.Now()}, CreatorTitle: "Person <bio>", RoomName: "Room & name", HTML: template.HTML("<p>trusted &amp; sanitized</p>"), Permalink: "https://example.test/rooms/456/@123?x=1&y=2", CreatorUpdatedAt: time.Now()}
	cases := map[string]func(*messageView){
		"plain": func(v *messageView) {},
		"emoji": func(v *messageView) { v.AllEmoji = true },
		"attachment": func(v *messageView) {
			v.Attachment = &storage.Blob{Filename: "file <name>.jpg"}
			v.BlobURL = "https://example.test/file?a=1&b=2"
			v.DownloadURL = v.BlobURL + "&disposition=attachment"
		},
		"emoji attachment with unusual URLs": func(v *messageView) {
			v.AllEmoji = true
			v.Attachment = &storage.Blob{Filename: "file \"<>'&\x00.jpg"}
			v.BlobURL = "javascript: \"<>'&\x00é"
			v.DownloadURL = "javascript:alert(1)"
		},
		"boosts": func(v *messageView) { v.Boosts = []database.Boost{{ID: 42, MessageID: v.ID, Content: "👍"}} },
		"unusual text": func(v *messageView) {
			v.ClientID = "\x00\"'><&=foo"
			v.Creator = "\x00<script>alert('x')</script>"
			v.CreatorTitle = "\x00<>&\"'"
			v.RoomName = "\x00<>&\"'"
		},
		"unsafe URL":          func(v *messageView) { v.Permalink = "javascript:alert(1)" },
		"URL normalization":   func(v *messageView) { v.Permalink = "https://example.test/a b?x=\"\x00'<>é" },
		"no avatar timestamp": func(v *messageView) { v.CreatorUpdatedAt = time.Time{} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := base
			change(&v)
			var expected bytes.Buffer
			if err := app.templates.ExecuteTemplate(&expected, "message-uncached", v); err != nil {
				t.Fatal(err)
			}
			actual, err := app.messageMarkup(v)
			if err != nil {
				t.Fatal(err)
			}
			if actual != expected.String() {
				for i := range min(len(actual), expected.Len()) {
					if actual[i] != expected.Bytes()[i] {
						t.Fatalf("byte %d: got %q; want %q", i, actual[max(0, i-30):min(len(actual), i+90)], expected.String()[max(0, i-30):min(expected.Len(), i+90)])
					}
				}
				t.Fatalf("body length: got %d, want %d", len(actual), expected.Len())
			}
		})
	}
}

func TestSearchShellPreservesCountAndFreshData(t *testing.T) {
	app, _, _, user := testApp(t)
	base := page{User: user, Screen: "search", Query: "coffee & tea", Origin: "https://example.test", MessagesHTML: template.HTML("<p>messages</p>"), Messages: make([]messageView, 3), ReturnRoom: 123}
	for _, count := range []int{3, 3, 1, 0} {
		p := base
		p.Messages = make([]messageView, count)
		var expected bytes.Buffer
		if err := app.templates.ExecuteTemplate(&expected, "search", p); err != nil {
			t.Fatal(err)
		}
		shell, marker, err := app.pageShell("search", p)
		if err != nil {
			t.Fatal(err)
		}
		if actual := strings.ReplaceAll(shell, marker, string(p.MessagesHTML)); actual != expected.String() {
			t.Fatal("search shell differs from template")
		}
		// Cached pages carry database records without allocating message views.
		p.Messages = nil
		p.messageRecords = make([]database.Message, count)
		shell, marker, err = app.pageShell("search", p)
		if err != nil {
			t.Fatal(err)
		}
		if actual := strings.ReplaceAll(shell, marker, string(p.MessagesHTML)); actual != expected.String() {
			t.Fatal("deferred search views changed result count")
		}
	}
}
