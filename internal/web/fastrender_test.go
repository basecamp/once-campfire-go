package web

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

// goldenViews is the ENGINE-32 corpus: the message shapes the fragment
// renderer must reproduce byte-identically against html/template.
func goldenViews(app *Server) []messageView {
	now := time.Date(2026, 10, 6, 12, 30, 45, 123456000, time.UTC)
	jpeg := "image/jpeg"
	pdf := "application/pdf"
	remote := "https://cdn.example.test"
	plain := `<p>Hello world</p>`
	rich := `<p>Hey <strong>@alice</strong>, check <a href="https://x.test/a?b=1&amp;c=2" data-foo="&quot;">this</a> 🚀</p><pre><code>if x &lt; 1 { log("x=&lt;" + x) }</code></pre>`
	views := []messageView{
		{ // plain text message
			AllEmoji:     false,
			Message:      database.Message{ID: 1, RoomID: 2, CreatorID: 3, ClientID: "cl-abc", Body: "Hello world", Creator: "Alice", CreatedAt: now, UpdatedAt: now},
			HTML:         template.HTML(plain),
			Permalink:    "http://example.org/rooms/2/@1",
			CreatorTitle: "Alice",
			RoomName:     "General",
		},
		{ // quoting and markup specials in every user-controlled field
			AllEmoji:     false,
			Message:      database.Message{ID: 7, RoomID: 8, CreatorID: 9, ClientID: `we"ird&<x>'+z`, Body: "x", Creator: `A<b>&"c'`, CreatedAt: now, UpdatedAt: now},
			HTML:         template.HTML(`<p>hi &amp; <b>&quot;bye&quot;</b></p>`),
			Permalink:    `https://ex.test/p?a=1&b="2"+3'4`,
			CreatorTitle: `A<b>&"c'`,
			RoomName:     `Ro"om & <c> 'x' +y`,
		},
		{ // all-emoji message (message--emoji class)
			AllEmoji:     true,
			Message:      database.Message{ID: 2, RoomID: 2, CreatorID: 3, ClientID: "cl-em", Body: "🎉✨👍", Creator: "Alice", CreatedAt: now, UpdatedAt: now},
			HTML:         template.HTML(`<p>🎉✨👍</p>`),
			Permalink:    "http://example.org/rooms/2/@2",
			CreatorTitle: "Alice",
			RoomName:     "General",
		},
		{ // unicode names and ids
			AllEmoji:     true,
			Message:      database.Message{ID: 3, RoomID: 4, CreatorID: 5, ClientID: "日本語🎌", Body: "こんにちは", Creator: "さくら", CreatedAt: now, UpdatedAt: now.Add(5 * time.Minute)},
			HTML:         template.HTML(`<p>こんにちは 🌸</p>`),
			Permalink:    "http://example.org/rooms/4/@3",
			CreatorTitle: "さくら（桜）",
			RoomName:     "ルーム「雑談」",
		},
		{ // zero timestamps
			AllEmoji:     false,
			Message:      database.Message{ID: 4, RoomID: 5, CreatorID: 6, ClientID: "zero", Body: "x", Creator: "Alice", CreatedAt: time.Time{}, UpdatedAt: time.Time{}},
			HTML:         template.HTML(rich),
			Permalink:    "http://example.org/rooms/5/@4",
			CreatorTitle: "Alice",
			RoomName:     "Z",
		},
		{ // sound-style presentation (soundHTML output)
			AllEmoji:     false,
			Message:      database.Message{ID: 5, RoomID: 2, CreatorID: 3, ClientID: "cl-snd", Body: "/play bell", Creator: "Alice", CreatedAt: now, UpdatedAt: now},
			HTML:         template.HTML(`<div class="sound" data-controller="sound" data-action="messages:play-&gt;sound#play" data-sound-url-value="/assets/bell.mp3"><button class="btn btn--plain" data-action="sound#play">🔊</button>🔔</div>`),
			Permalink:    "http://example.org/rooms/2/@5",
			CreatorTitle: "Alice",
			RoomName:     "General",
		},
		{ // image attachment
			AllEmoji:     false,
			Message:      database.Message{ID: 6, RoomID: 2, CreatorID: 3, ClientID: "cl-img", Body: "", Creator: "Alice", CreatedAt: now, UpdatedAt: now},
			Attachment:   &storage.Blob{Filename: "photo & friends.jpg", ContentType: &jpeg, ByteSize: 12345},
			BlobURL:      "/rails/active_storage/blobs/redirect/tok/photo%20%26%20friends.jpg",
			DownloadURL:  "/rails/active_storage/blobs/redirect/tok/photo%20%26%20friends.jpg?disposition=attachment",
			PreviewURL:   "/rails/active_storage/representations/redirect/tok/photo.webp",
			Image:        true,
			HTML:         template.HTML(`<div class="max-inline-size center flex overflow-clip" style="width: 600px; aspect-ratio: 1.5;"><a class="flex" data-lightbox-target="image" data-action="lightbox#open" data-lightbox-url-value="/rails/active_storage/blobs/redirect/tok/photo%20%26%20friends.jpg?disposition=attachment" href="/rails/active_storage/blobs/redirect/tok/photo%20%26%20friends.jpg"><img width="1200" height="800" class="message__attachment" loading="lazy" src="/rails/active_storage/representations/redirect/tok/photo.webp" /></a></div>`),
			Permalink:    "http://example.org/rooms/2/@6",
			CreatorTitle: "Alice",
			RoomName:     "General",
		},
		{ // file attachment with hostile filename and remote blob urls
			AllEmoji:     false,
			Message:      database.Message{ID: 9, RoomID: 2, CreatorID: 3, ClientID: "cl-fil", Body: "", Creator: "Alice", CreatedAt: now, UpdatedAt: now},
			Attachment:   &storage.Blob{Filename: `we"ir<d>& 'a'.pdf`, ContentType: &pdf, ByteSize: 42},
			BlobURL:      remote + "/rails/active_storage/blobs/redirect/tok/a%20b?x=1&y=2",
			DownloadURL:  remote + "/rails/active_storage/blobs/redirect/tok/a%20b?x=1&y=2&disposition=attachment",
			HTML:         template.HTML(`<div class="flex-inline align-center gap-half"><img class="colorize--black" aria-hidden="true" src="/assets/common-file-text.svg" width="22" height="22" /><span>we&#45;ir&#60;d&#62;&#38; &#39;a&#39;.pdf</span><a class="btn message__action-btn hide-in-ios-pwa" style="--width: auto;" href="/rails/active_storage/blobs/redirect/tok/a%20b?x=1&amp;y=2&amp;disposition=attachment"><img aria-hidden="true" src="/assets/download.svg" width="20" height="20" /><span class="for-screen-reader">Download we&#45;ir&#60;d&#62;&#38; &#39;a&#39;.pdf</span></a><button class="btn message__action-btn" style="--width: auto;" data-controller="web-share" data-action="web-share#share" data-web-share-files-value="/rails/active_storage/blobs/redirect/tok/a%20b?x=1&amp;y=2&amp;disposition=attachment"><img aria-hidden="true" src="/assets/share.svg" width="20" height="20" /><span class="for-screen-reader">Share we&#45;ir&#60;d&#62;&#38; &#39;a&#39;.pdf</span></button></div>`),
			Permalink:    "http://example.org/rooms/2/@9",
			CreatorTitle: "Alice",
			RoomName:     "General",
		},
		{ // boosts: single, multiple, all-emoji, specials
			AllEmoji:     false,
			Message:      database.Message{ID: 10, RoomID: 2, CreatorID: 3, ClientID: "cl-boo", Body: "x", Creator: "Alice", CreatedAt: now, UpdatedAt: now},
			HTML:         template.HTML(plain),
			Permalink:    "http://example.org/rooms/2/@10",
			CreatorTitle: "Alice",
			RoomName:     "General",
			Boosts: []database.Boost{
				{BoosterTitle: "Bob", BoosterUpdatedAt: now, ID: 1, MessageID: 10, BoosterID: 4, Content: "nice 👍", Booster: "Bob", CreatedAt: now, UpdatedAt: now},
				{BoosterTitle: `C<&"'le`, BoosterUpdatedAt: now, ID: 2, MessageID: 10, BoosterID: 5, Content: "🎉✨", Booster: `C<&"'le`, CreatedAt: now, UpdatedAt: now},
				{BoosterTitle: "Dave", BoosterUpdatedAt: now, ID: 3, MessageID: 10, BoosterID: 6, Content: `a<b>&"c'`, Booster: "Dave", CreatedAt: now, UpdatedAt: now},
			},
		},
		{ // no boosts at all
			AllEmoji:     false,
			Message:      database.Message{ID: 11, RoomID: 2, CreatorID: 3, ClientID: "cl-nob", Body: "x", Creator: "Alice", CreatedAt: now, UpdatedAt: now},
			HTML:         template.HTML(plain),
			Permalink:    "http://example.org/rooms/2/@11",
			CreatorTitle: "Alice",
			RoomName:     "General",
			Boosts:       []database.Boost{},
		},
		{ // NUL byte and noncharacters in attribute fields
			AllEmoji:     false,
			Message:      database.Message{ID: 12, RoomID: 2, CreatorID: 3, ClientID: "nul\x00byte", Body: "x", Creator: "\uFDD0\uFFF0x", CreatedAt: now, UpdatedAt: now},
			HTML:         template.HTML(plain),
			Permalink:    "http://example.org/rooms/2/@12",
			CreatorTitle: "t\uFDD0i",
			RoomName:     "n\uFFF0ame",
		},
		{ // invalid UTF-8 passes through byte-identically
			AllEmoji:     false,
			Message:      database.Message{ID: 13, RoomID: 2, CreatorID: 3, ClientID: "bad\xff\xfe", Body: "x", Creator: "A\xff", CreatedAt: now, UpdatedAt: now},
			HTML:         template.HTML(plain),
			Permalink:    "http://example.org/rooms/2/@13",
			CreatorTitle: "B\xc3",
			RoomName:     "c\x80",
		},
		{ // newlines and tabs in attribute values are not escaped
			AllEmoji:     false,
			Message:      database.Message{ID: 14, RoomID: 2, CreatorID: 3, ClientID: "line1\nline2\ttab", Body: "x", Creator: "A\nB", CreatedAt: now, UpdatedAt: now},
			HTML:         template.HTML(plain),
			Permalink:    "http://example.org/rooms/2/@14",
			CreatorTitle: "multi\nline\ttitle",
			RoomName:     "room\nname",
		},
	}
	return views
}

func TestFastRenderGolden(t *testing.T) {
	app, _, _, _ := testApp(t)
	r := app.fastRender
	if r == nil {
		t.Fatal("fast renderer not compiled")
	}
	tm := app.templates.Lookup("message-uncached")
	if tm == nil {
		t.Fatal("message-uncached missing")
	}
	for i, v := range goldenViews(app) {
		want, err := app.markup("message-uncached", v)
		if err != nil {
			t.Fatalf("view %d: oracle: %v", i, err)
		}
		got := string(r.render(nil, &v))
		if got != want {
			t.Errorf("view %d (%s): compiled != html/template\n--- compiled (%d bytes) ---\n%s\n--- html/template (%d bytes) ---\n%s", i, v.ClientID, len(got), got, len(want), want)
		}
	}
}

// TestFastRenderConcurrent renders the corpus from many goroutines against
// the precomputed oracle; runs under -race in the acceptance gate.
func TestFastRenderConcurrent(t *testing.T) {
	app, _, _, _ := testApp(t)
	r := app.fastRender
	if r == nil {
		t.Fatal("fast renderer not compiled")
	}
	views := goldenViews(app)
	want := make([][]byte, len(views))
	for i := range views {
		s, err := app.markup("message-uncached", views[i])
		if err != nil {
			t.Fatal(err)
		}
		want[i] = []byte(s)
	}
	var wg sync.WaitGroup
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for iter := 0; iter < 40; iter++ {
				for i := range views {
					var dst []byte
					dst = r.render(dst, &views[i])
					if !bytes.Equal(dst, want[i]) {
						t.Errorf("goroutine %d view %d: mismatch", g, i)
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestFastRenderMultibyteBuffer(t *testing.T) {
	// Rendering into a pre-filled caller buffer must append, not overwrite.
	app, _, _, _ := testApp(t)
	r := app.fastRender
	if r == nil {
		t.Fatal("fast renderer not compiled")
	}
	v := goldenViews(app)[0]
	want, err := app.markup("message-uncached", v)
	if err != nil {
		t.Fatal(err)
	}
	dst := []byte("prefix<")
	out := string(r.render(dst, &v))
	if !strings.HasPrefix(out, "prefix<") || !strings.HasSuffix(out, want) {
		t.Fatalf("render into populated buffer: got %d bytes, want prefix+%d", len(out), len(want))
	}
}

func TestFastRenderFlag(t *testing.T) {
	for _, c := range []struct {
		value          string
		enabled, valid bool
	}{
		{"", true, true},
		{"on", true, true},
		{"true", true, true},
		{"1", true, true},
		{"ON", true, true},
		{"off", false, true},
		{"false", false, true},
		{"0", false, true},
		{"OFF", false, true},
		{"sometimes", true, false},
		{" ", true, true},
	} {
		enabled, valid := parseFastRender(c.value)
		if enabled != c.enabled || valid != c.valid {
			t.Errorf("parseFastRender(%q) = (%v, %v), want (%v, %v)", c.value, enabled, valid, c.enabled, c.valid)
		}
	}
}

// TestFastRenderFlagOff verifies the off switch reverts messageViews to the
// html/template path (byte-identity of the two paths is the golden corpus's
// job; here the renderer must be absent and the route must still render).
func TestFastRenderFlagOff(t *testing.T) {
	t.Setenv("CAMPFIRE_FAST_RENDER", "off")
	app, _, _, user := testApp(t)
	if app.fastRender != nil {
		t.Fatal("fast renderer compiled with CAMPFIRE_FAST_RENDER=off")
	}
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "flag-off", "<p>flag off body</p>", "flag off body")
	if err != nil {
		t.Fatal(err)
	}
	views, err := app.messageViews(ctx, []database.Message{m})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(views[0].Fragment), "flag off body") {
		t.Fatalf("flag-off fragment missing body: %q", views[0].Fragment)
	}
	if got, err := app.markup("message-uncached", views[0]); err != nil || got != string(views[0].Fragment) {
		t.Fatalf("flag-off fragment != template output (err=%v)", err)
	}
}

// TestFastRenderBroadcast renders through the create-message route and checks
// the turbo-stream response — the same bytes the cable publish sends —
// carries the compiled fragment exactly once. This is the render-once/reuse
// property ENGINE-32 requires of the broadcast path.
func TestFastRenderBroadcast(t *testing.T) {
	app, server, cookie, user := testApp(t)
	if app.fastRender == nil {
		t.Fatal("fast renderer not compiled")
	}
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	form := url.Values{"message[client_message_id]": {`turbo"<id>&`}, "message[body]": {"Hello broadcast"}}
	response, body := perform(t, server, "POST", fmt.Sprintf("/rooms/%d/messages", room.ID), "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), cookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST status %d: %s", response.StatusCode, body)
	}
	published := string(body)
	head := `<turbo-stream action="append" target="` + room.DOM("messages") + `"><template>`
	tail := `</template></turbo-stream>`
	if !strings.HasPrefix(published, head) || !strings.HasSuffix(published, tail) {
		t.Fatalf("unexpected turbo-stream shape:\n%s", published)
	}
	fragment := published[len(head) : len(published)-len(tail)]
	// The same fragment must come out of messageViews -> fastrender. Since
	// the observed-generation namespacing and origin-scoped fragments were
	// adopted, a fragment stored under one request's (generation, host,
	// origin) is only served to a request with the same metadata; a bare
	// background context renders the example.org fallback origin instead, so
	// the comparison carries the same request metadata the POST had.
	created, err := app.DB.Messages(ctx, room.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) == 0 {
		t.Fatal("no message created")
	}
	version, err := app.DB.ResponseVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	comparison := httptest.NewRequest("GET", "/", nil)
	info := &requestInfo{host: strings.TrimPrefix(server.URL, "http://"), origin: server.URL, databaseVersion: version}
	comparison = comparison.WithContext(context.WithValue(comparison.Context(), requestInfoKey{}, info))
	comparison = comparison.WithContext(context.WithValue(comparison.Context(), requestOriginKey{}, info.origin))
	views, err := app.messageViews(comparison.Context(), []database.Message{created[len(created)-1]})
	if err != nil {
		t.Fatal(err)
	}
	if string(views[0].Fragment) != fragment {
		t.Fatalf("broadcast fragment differs from messageViews fragment:\n--- broadcast ---\n%s\n--- views ---\n%s", fragment, views[0].Fragment)
	}
}
