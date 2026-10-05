package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
	"golang.org/x/crypto/bcrypt"
)

func TestBroadcastsRenderTurboStreamActionsLikeTurboRails(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{turboStreamAction("append", "messages_rooms_open_1", `<div id="m">Hi &amp; bye</div>`, false),
			`<turbo-stream action="append" target="messages_rooms_open_1"><template><div id="m">Hi &amp; bye</div></template></turbo-stream>`},
		{turboStreamAction("remove", "message_1", "", false), `<turbo-stream action="remove" target="message_1"></turbo-stream>`},
		{turboStreamAction("replace", "presentation_message_1", "x", true),
			`<turbo-stream maintain_scroll="true" action="replace" target="presentation_message_1"><template>x</template></turbo-stream>`},
	} {
		if c.got != c.want {
			t.Errorf("got  %s\nwant %s", c.got, c.want)
		}
	}
}

func TestSearchQueriesKeepOnlyOnigmoWordCharacters(t *testing.T) {
	for q, want := range map[string]string{
		"héllo wörld_1 ２ 日本語 ‿ a-b ️ ❤ é": "héllo wörld_1 ２ 日本語 ‿ a b ️   é",
		`"quoted" OR NEAR(x*)`:            " quoted  OR NEAR x  ",
		"ǅʰⅫⒶ‍½²€":                        "ǅʰⅫⒶ‍   ",
	} {
		if got := *searchQuery(&q); got != want {
			t.Errorf("searchQuery(%q) = %q, want %q", q, got, want)
		}
	}
	if searchQuery(nil) != nil {
		t.Error("no q")
	}
}

// roomsTest is a signed-in David with his first room, and the room's messages stream.
type roomsTest struct {
	t      *testing.T
	ts     *httptest.Server
	db     *database.DB
	app    *Server
	cookie *http.Cookie
	room   database.Room
	cable  *websocket.Conn
}

func newRoomsTest(t *testing.T) *roomsTest {
	db, err := database.Open(filepath.Join(t.TempDir(), "app.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("test-only-secret")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ts := httptest.NewServer(app)
	t.Cleanup(ts.Close)
	digest, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.Setup(context.Background(), "David", "david@test", string(digest))
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	test := &roomsTest{t: t, ts: ts, db: db, app: app, room: rooms[0]}
	response := test.do("POST", "/session", url.Values{"email_address": {"david@test"}, "password": {"correct horse"}}.Encode(), nil)
	for _, c := range response.Cookies() {
		if c.Name == "session_token" {
			test.cookie = c
		}
	}
	if test.cookie == nil {
		t.Fatal("no session cookie")
	}
	return test
}

func (rt *roomsTest) do(method, path, body string, header http.Header) *http.Response {
	rt.t.Helper()
	r, err := http.NewRequest(method, rt.ts.URL+path, strings.NewReader(body))
	if err != nil {
		rt.t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", rt.ts.URL)
	for key, values := range header {
		r.Header[key] = values
	}
	if rt.cookie != nil {
		r.AddCookie(rt.cookie)
	}
	client := rt.ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(r)
	if err != nil {
		rt.t.Fatal(err)
	}
	rt.t.Cleanup(func() { response.Body.Close() })
	return response
}

func (rt *roomsTest) body(response *http.Response) string {
	rt.t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		rt.t.Fatal(err)
	}
	return string(data)
}

// subscribe listens to the room's messages stream (RoomMessagesChannel).
func (rt *roomsTest) subscribe() {
	rt.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(rt.ts.URL, "http")+"/cable", &websocket.DialOptions{
		Subprotocols: []string{"actioncable-v1-json"},
		HTTPHeader:   http.Header{"Origin": {rt.ts.URL}, "Cookie": {rt.cookie.String()}},
	})
	if err != nil {
		rt.t.Fatal(err)
	}
	rt.t.Cleanup(func() { conn.CloseNow() })
	identifier, _ := json.Marshal(map[string]string{"channel": "RoomMessagesChannel",
		"signed_stream_name": rt.app.Secrets.SignStream(rails.RoomStream(rt.room.Type, rt.room.ID))})
	command, _ := json.Marshal(map[string]string{"command": "subscribe", "identifier": string(identifier)})
	if err := conn.Write(ctx, websocket.MessageText, command); err != nil {
		rt.t.Fatal(err)
	}
	rt.cable = conn
	for {
		var frame struct{ Type string }
		_, data, err := conn.Read(ctx)
		if err != nil {
			rt.t.Fatal(err)
		}
		if json.Unmarshal(data, &frame) == nil && frame.Type == "confirm_subscription" {
			return
		}
	}
}

// broadcast is the next turbo stream broadcast to the room.
func (rt *roomsTest) broadcast() string {
	rt.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := rt.cable.Read(ctx)
		if err != nil {
			rt.t.Fatal(err)
		}
		var frame struct{ Message any }
		if json.Unmarshal(data, &frame) == nil {
			if markup, ok := frame.Message.(string); ok {
				return markup
			}
		}
	}
}

var turboStream = http.Header{"Accept": {"text/vnd.turbo-stream.html, text/html, application/xhtml+xml"}}

func TestRoomsShowRemembersTheRoomOrSendsYouBackToTheRoot(t *testing.T) {
	rt := newRoomsTest(t)
	response := rt.do("GET", fmt.Sprintf("/rooms/%d", rt.room.ID), "", nil)
	page := rt.body(response)
	if response.StatusCode != 200 || !strings.Contains(page, fmt.Sprintf(`<meta name="current-room-id" content="%d">`, rt.room.ID)) {
		t.Fatalf("room page: %d %s", response.StatusCode, page)
	}
	remembered := false
	for _, c := range response.Cookies() {
		remembered = remembered || c.Name == "last_room" && c.Value == fmt.Sprint(rt.room.ID)
	}
	if !remembered {
		t.Error("last_room not remembered")
	}
	response = rt.do("GET", "/rooms/999999", "", nil)
	if response.StatusCode != 302 || response.Header.Get("Location") != rt.ts.URL+"/" {
		t.Errorf("inaccessible room: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
}

func TestMessageWritesAnswerAndBroadcastAsTheReferenceDoes(t *testing.T) {
	rt := newRoomsTest(t)
	rt.subscribe()
	messages := fmt.Sprintf("/rooms/%d/messages", rt.room.ID)

	response := rt.do("POST", messages, "message%5Bbody%5D=Hello+%3Cb%3Ethere%3C%2Fb%3E&message%5Bclient_message_id%5D=client-1", turboStream)
	created := rt.body(response)
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != turboStreamContentType {
		t.Fatalf("create: %d %s", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if want := fmt.Sprintf(`<turbo-stream action="append" target="messages_rooms_open_%d"><template>`+"\n  "+`<div id="message_client-1"`, rt.room.ID); !strings.HasPrefix(created, want) {
		t.Fatalf("create stream: %s", created)
	}
	if broadcast := rt.broadcast(); broadcast != created {
		t.Errorf("broadcast_create differs from the response:\n%s\n%s", broadcast, created)
	}
	id := regexp.MustCompile(`data-message-id="(\d+)"`).FindStringSubmatch(created)[1]

	response = rt.do("GET", messages, "", nil)
	if page := rt.body(response); response.StatusCode != 200 || !strings.Contains(page, "Hello <b>there</b>") {
		t.Fatalf("index: %d %s", response.StatusCode, page)
	}
	response = rt.do("GET", messages, "", http.Header{"If-None-Match": {response.Header.Get("ETag")}})
	if response.StatusCode != 304 {
		t.Errorf("fresh_when: %d", response.StatusCode)
	}

	response = rt.do("POST", messages+"/"+id, "_method=patch&message%5Bbody%5D=Edited", http.Header{"Accept": {"text/html"}})
	if response.StatusCode != 302 || response.Header.Get("Location") != rt.ts.URL+messages+"/"+id {
		t.Fatalf("update: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	if broadcast := rt.broadcast(); !strings.HasPrefix(broadcast, `<turbo-stream maintain_scroll="true" action="replace" target="presentation_message_client-1"><template><div id="presentation_message_client-1"`) ||
		!strings.Contains(broadcast, "Edited") {
		t.Errorf("broadcast_replace: %s", broadcast)
	}

	response = rt.do("POST", "/messages/"+id+"/boosts", "boost%5Bcontent%5D=%F0%9F%94%A5", nil)
	if response.StatusCode != 302 || response.Header.Get("Location") != rt.ts.URL+"/messages/"+id+"/boosts" {
		t.Fatalf("boost: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	boost := rt.broadcast()
	if !strings.HasPrefix(boost, `<turbo-stream maintain_scroll="true" action="append" target="boosts_message_client-1"><template><div id="boost_`) {
		t.Errorf("boost broadcast_create: %s", boost)
	}
	boostID := regexp.MustCompile(`id="boost_(\d+)"`).FindStringSubmatch(boost)[1]
	if response = rt.do("POST", "/messages/"+id+"/boosts/"+boostID, "_method=delete", nil); response.StatusCode != 204 {
		t.Fatalf("unboost: %d", response.StatusCode)
	}
	if broadcast := rt.broadcast(); broadcast != `<turbo-stream action="remove" target="boost_`+boostID+`"></turbo-stream>` {
		t.Errorf("boost broadcast_remove: %s", broadcast)
	}

	response = rt.do("POST", messages+"/"+id, "_method=delete", turboStream)
	removed := `<turbo-stream action="remove" target="message_client-1"></turbo-stream>`
	if body := rt.body(response); response.StatusCode != 200 || body != removed {
		t.Fatalf("destroy: %d %s", response.StatusCode, body)
	}
	if broadcast := rt.broadcast(); broadcast != removed {
		t.Errorf("broadcast_remove: %s", broadcast)
	}
	if response = rt.do("GET", messages, "", nil); response.StatusCode != 204 {
		t.Errorf("empty room: %d", response.StatusCode)
	}
}

func TestMessageWritesFailAsTheReferenceDoes(t *testing.T) {
	rt := newRoomsTest(t)
	messages := fmt.Sprintf("/rooms/%d/messages", rt.room.ID)
	if response := rt.do("POST", messages, "other=1", turboStream); response.StatusCode != 400 || rt.body(response) != "" {
		t.Errorf("params.require(:message): %d", response.StatusCode)
	}
	response := rt.do("POST", "/rooms/999999/messages", "message%5Bbody%5D=Hi", turboStream)
	if page := rt.body(response); response.StatusCode != 200 || !strings.Contains(page, "This room was deleted.") {
		t.Errorf("room_not_found: %d %s", response.StatusCode, page)
	}
	if response := rt.do("GET", messages+"?before=999999", "", nil); response.StatusCode != 404 {
		t.Errorf("missing anchor: %d", response.StatusCode)
	}
	if response := rt.do("POST", messages, "message%5Bbody%5D=Hi", http.Header{"Accept": {"text/html"}}); response.StatusCode != 406 {
		t.Errorf("create as HTML: %d", response.StatusCode)
	}
}

func TestSearchesAndInvolvementsRedirectAsTheReferenceDoes(t *testing.T) {
	rt := newRoomsTest(t)
	response := rt.do("POST", "/searches", "q=hello%2C+world", nil)
	if response.StatusCode != 302 || response.Header.Get("Location") != rt.ts.URL+"/searches?q=hello++world" {
		t.Errorf("record: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	response = rt.do("GET", "/searches", "", nil)
	if page := rt.body(response); !strings.Contains(page, `href="/searches?q=hello++world"`) {
		t.Errorf("recent searches: %s", page)
	}
	if response := rt.do("POST", "/searches", "other=1", nil); response.StatusCode != 500 {
		t.Errorf("a search without a query: %d", response.StatusCode)
	}
	response = rt.do("POST", "/searches/clear", "_method=delete", nil)
	if response.StatusCode != 302 || response.Header.Get("Location") != rt.ts.URL+"/searches" {
		t.Errorf("clear: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	if page := rt.body(rt.do("GET", "/searches", "", nil)); strings.Contains(page, "hello") {
		t.Error("searches not cleared")
	}

	involvement := fmt.Sprintf("/rooms/%d/involvement", rt.room.ID)
	response = rt.do("POST", involvement, "_method=patch&involvement=nothing", nil)
	if response.StatusCode != 302 || response.Header.Get("Location") != rt.ts.URL+involvement {
		t.Errorf("involvement: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	if page := rt.body(rt.do("GET", involvement, "", nil)); !strings.Contains(page, "Notifications are off") {
		t.Errorf("involvement show: %s", page)
	}
	if response := rt.do("POST", involvement, "_method=patch&involvement=bogus", nil); response.StatusCode != 500 {
		t.Errorf("invalid involvement: %d", response.StatusCode)
	}
}

// A message whose creator is gone renders messages/_unrenderable in its place, and the page
// around it still renders.
func TestMissingMessageAuthorRendersUnrenderable(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "orphan", "hello", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Write.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Write.Exec("UPDATE messages SET creator_id=999999 WHERE id=?", message.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Write.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	response, body := perform(t, server, "GET", fmt.Sprintf("/rooms/%d/messages", rooms[0].ID), "", nil, cookie)
	if response.StatusCode != 200 || !strings.Contains(string(body), `message--failed`) || !strings.Contains(string(body), "Failed to load message content") {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
}

// A bot's webhook reply is broadcast_create'd outside the request: messages/_message appended to
// the room, after the message that triggered it.
func TestBotWebhookReplyIsBroadcast(t *testing.T) {
	rt := newRoomsTest(t)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<p>Bot reply</p>")
	}))
	t.Cleanup(webhook.Close)
	ctx := context.Background()
	endpoint := webhook.URL
	bot, err := rt.db.CreateUser(ctx, "Reply Bot", "", "", "", 2, &endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if rt.room, err = rt.db.CreateRoom(ctx, rt.room.CreatorID, "Rooms::Direct", "", []int64{bot.ID}); err != nil {
		t.Fatal(err)
	}
	rt.subscribe()
	response := rt.do("POST", fmt.Sprintf("/rooms/%d/messages", rt.room.ID), "message%5Bbody%5D=hello&message%5Bclient_message_id%5D=client-1", turboStream)
	if response.StatusCode != 200 {
		t.Fatalf("create: %d %s", response.StatusCode, rt.body(response))
	}
	target := fmt.Sprintf(`<turbo-stream action="append" target="messages_rooms_direct_%d"><template>`, rt.room.ID)
	if created := rt.broadcast(); !strings.HasPrefix(created, target) || !strings.Contains(created, `id="message_client-1"`) {
		t.Fatalf("broadcast_create: %s", created)
	}
	reply := rt.broadcast()
	if !strings.HasPrefix(reply, target) || !strings.Contains(reply, "Bot reply") || !strings.Contains(reply, fmt.Sprintf(`data-user-id="%d"`, bot.ID)) {
		t.Fatalf("reply broadcast: %s", reply)
	}
}
