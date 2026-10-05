package cable

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
)

// testConnection is a connection with no socket: what's queued for it stays in its queue.
func testConnection(h *Hub, user int64) *connection {
	return &connection{hub: h, user: database.User{ID: user}, wake: make(chan struct{}, 1), subscriptions: map[string]*subscription{}}
}

func subscribeTo(h *Hub, c *connection, stream, identifier string) *subscription {
	s := &subscription{conn: c, identifier: identifier, stream: stream}
	c.subscriptions[identifier] = s
	h.join(s)
	return s
}

func queued(c *connection) []*websocket.PreparedMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	var frames []*websocket.PreparedMessage
	for _, d := range c.queue {
		frames = append(frames, d.frame)
	}
	return frames
}

func TestBroadcastsGoToTheirStreamOnceWrappedPerIdentifier(t *testing.T) {
	h := New(nil, nil)
	a, b, other, elsewhere := testConnection(h, 1), testConnection(h, 2), testConnection(h, 3), testConnection(h, 4)
	sa := subscribeTo(h, a, "rooms", "turbo")
	sb := subscribeTo(h, b, "rooms", "turbo")
	so := subscribeTo(h, other, "rooms", "other")
	subscribeTo(h, elsewhere, "user_4_unreads", "turbo")

	if reached := h.broadcast("rooms", []byte(`"x"`)); reached != 3 {
		t.Fatalf("reached %d subscribers, want 3", reached)
	}
	fa, fb, fo := queued(a), queued(b), queued(other)
	if len(fa) != 1 || len(fb) != 1 || len(fo) != 1 || len(queued(elsewhere)) != 0 {
		t.Fatal("delivered to the wrong subscribers")
	}
	if fa[0] != fb[0] {
		t.Fatal("subscribers with one identifier should share one frame")
	}
	if fa[0] == fo[0] {
		t.Fatal("another identifier needs its own frame")
	}
	if h.broadcast("nobody", []byte(`"x"`)) != 0 {
		t.Fatal("a stream without subscribers reached someone")
	}

	h.leave(sa)
	h.leave(so)
	if groups := h.streams["rooms"]; len(groups) != 1 || len(groups[0].members) != 1 || groups[0].members[0] != sb || sb.index != 0 {
		t.Fatalf("after leaving: %+v", groups)
	}
	h.leave(sb)
	if _, ok := h.streams["rooms"]; ok || len(h.streams) != 1 {
		t.Fatal("an empty stream should be dropped")
	}
}

// Like the reference's per-stream ring buffers: a subscription may be streamCapacity frames
// behind before its connection closes with reconnect: true, however many streams it has.
func TestLaggingSubscriptionClosesItsConnection(t *testing.T) {
	h := New(nil, nil)
	c := testConnection(h, 1)
	subscribeTo(h, c, "one", "a")
	two := subscribeTo(h, c, "two", "a")
	for range streamCapacity {
		h.broadcast("one", []byte("1"))
		h.broadcast("two", []byte("2"))
	}
	if c.closing != nil || len(c.queue) != 2*streamCapacity {
		t.Fatalf("closing %v with %d queued: each stream may be %d behind", c.closing, len(c.queue), streamCapacity)
	}

	batch, closing := c.take(nil)
	if len(batch) != maxWriteBatch || closing != nil || two.pending != streamCapacity-maxWriteBatch/2 {
		t.Fatalf("took %d frames, %d still pending for the second stream", len(batch), two.pending)
	}
	for range maxWriteBatch / 2 {
		h.broadcast("two", []byte("2"))
	}
	if c.closing != nil {
		t.Fatal("frames taken for writing no longer count as behind")
	}
	h.broadcast("two", []byte("2"))
	if c.closing != lagged {
		t.Fatal("the stream that fell behind should close the connection")
	}
	queuedBefore := len(c.queue)
	h.broadcast("one", []byte("1"))
	c.transmit(welcome)
	if len(c.queue) != queuedBefore {
		t.Fatal("a closing connection queued more frames")
	}
}

func TestRemoteDisconnectsReachEveryConnectionOfTheUser(t *testing.T) {
	h := New(nil, nil)
	first, second, other := testConnection(h, 1), testConnection(h, 1), testConnection(h, 2)
	for _, c := range []*connection{first, second, other} {
		if !h.register(c) {
			t.Fatal("not registered")
		}
	}
	h.Reconnect(1)
	if first.closing != remoteReconnect || second.closing != remoteReconnect || other.closing != nil {
		t.Fatal("reset_remote_connections should close the user's connections only")
	}
	h.Disconnect(2)
	if other.closing != remoteFinal {
		t.Fatal("a ban or deactivation closes without reconnecting")
	}
	h.Disconnect(1)
	if first.closing != remoteReconnect {
		t.Fatal("a connection closes once, with the first reason")
	}
}

type fixture struct {
	t       *testing.T
	ctx     context.Context
	db      *database.DB
	hub     *Hub
	secrets *rails.Secrets
	owner   database.User
	room    database.Room
	token   string
	url     string
}

func newFixture(t *testing.T, configure ...func(*Hub)) *fixture {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	db, err := database.Open(filepath.Join(t.TempDir(), "cable.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	owner, err := db.Setup(ctx, "Owner", "owner@test", "unused")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.StartSession(ctx, owner.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := rails.NewSecrets("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, db: db, hub: New(db, secrets), secrets: secrets, owner: owner, room: rooms[0], token: token}
	for _, c := range configure {
		c(f.hub)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := owner
		if id := r.URL.Query().Get("user"); id != "" {
			json.Unmarshal([]byte(id), &user.ID)
		}
		token := token
		if r.URL.Query().Has("token") {
			token = r.URL.Query().Get("token")
		}
		f.hub.Serve(w, r, user, token)
	}))
	t.Cleanup(server.Close)
	// Cleanups run last first: clients close, then the hub, the server and the database.
	t.Cleanup(f.hub.Close)
	f.url = "ws" + strings.TrimPrefix(server.URL, "http")
	return f
}

// dial connects a client offering compression, as browsers do.
func (f *fixture) dial(query string) *websocket.Conn {
	f.t.Helper()
	conn, response, err := websocket.Dial(f.ctx, f.url+query, &websocket.DialOptions{Subprotocols: []string{"actioncable-v1-json"}, CompressionMode: websocket.CompressionNoContextTakeover})
	if err != nil {
		f.t.Fatal(err)
	}
	if !strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
		f.t.Fatal("compression was not negotiated", response.Header)
	}
	f.t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// connect dials and reads the welcome.
func (f *fixture) connect(query string) *websocket.Conn {
	f.t.Helper()
	conn := f.dial(query)
	f.expect(conn, `{"type":"welcome"}`)
	return conn
}

var pingFrame = regexp.MustCompile(`^\{"type":"ping","message":\d+\}$`)

// next reads the next text frame, skipping pings.
func (f *fixture) next(conn *websocket.Conn) string {
	f.t.Helper()
	for {
		_, data, err := conn.Read(f.ctx)
		if err != nil {
			f.t.Fatal(err)
		}
		if !pingFrame.Match(data) {
			return string(data)
		}
	}
}

func (f *fixture) expect(conn *websocket.Conn, want string) {
	f.t.Helper()
	if got := f.next(conn); got != want {
		f.t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func (f *fixture) send(conn *websocket.Conn, command map[string]string) {
	f.t.Helper()
	data, _ := json.Marshal(command)
	if err := conn.Write(f.ctx, websocket.MessageText, data); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) subscribe(conn *websocket.Conn, identifier, reply string) {
	f.t.Helper()
	f.send(conn, map[string]string{"command": "subscribe", "identifier": identifier})
	f.expect(conn, `{"identifier":`+jsonString(identifier)+`,"type":"`+reply+`"}`)
}

// expectClosed reads the rest of what the server sends: nothing but a normal close.
func (f *fixture) expectClosed(conn *websocket.Conn) {
	f.t.Helper()
	if _, data, err := conn.Read(f.ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		f.t.Fatalf("expected a normal close, read %s: %v", data, err)
	}
}

func (f *fixture) identifier(channel string, params map[string]any) string {
	params["channel"] = channel
	data, _ := json.Marshal(params)
	return string(data)
}

func (f *fixture) outsider() int64 {
	f.t.Helper()
	now := database.Stamp(time.Now())
	result, err := f.db.Write.ExecContext(f.ctx, "INSERT INTO users(name,email_address,status,role,created_at,updated_at) VALUES ('Outsider','outsider@test',0,0,?,?)", now, now)
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

// jsonString encodes s with encoding/json, which agrees with the reference for these ASCII
// identifiers.
func jsonString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

func jsonInt(n int64) string {
	data, _ := json.Marshal(n)
	return string(data)
}

func TestRoomMessagesAreAuthorizedWhenSubscribedAndRevokedByDisconnecting(t *testing.T) {
	f := newFixture(t)
	stranger := f.outsider()
	identifier := f.identifier("RoomMessagesChannel", map[string]any{"signed_stream_name": f.secrets.SignStream(rails.RoomStream(f.room.Type, f.room.ID))})
	owner, duplicate := f.connect(""), f.connect("")
	f.subscribe(owner, identifier, "confirm_subscription")
	f.subscribe(duplicate, identifier, "confirm_subscription")
	f.subscribe(f.connect("?user="+jsonInt(stranger)), identifier, "reject_subscription")

	body := strings.Repeat("hello ", 100) + lineSeparator
	f.hub.Publish(f.ctx, f.room.ID, `<turbo-stream action="append" target="messages"><template>`+body+`</template></turbo-stream>`)
	want := `{"identifier":` + jsonString(identifier) + `,"message":"` +
		htmlSafe.Replace(`<turbo-stream action=\"append\" target=\"messages\"><template>`) + body + htmlSafe.Replace(`</template></turbo-stream>`) + `"}`
	f.expect(owner, want)
	f.expect(duplicate, want)

	// Authorization happened at subscribe time: publishing runs no query, so a membership
	// removed behind the hub's back still receives, until the removal disconnects the user.
	if _, err := f.db.Write.ExecContext(f.ctx, "DELETE FROM memberships WHERE room_id=? AND user_id=?", f.room.ID, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	f.hub.Publish(f.ctx, f.room.ID, "after")
	f.expect(owner, `{"identifier":`+jsonString(identifier)+`,"message":"after"}`)
	f.expect(duplicate, `{"identifier":`+jsonString(identifier)+`,"message":"after"}`)

	f.hub.Reconnect(f.owner.ID)
	for _, conn := range []*websocket.Conn{owner, duplicate} {
		f.expect(conn, `{"type":"disconnect","reason":"remote","reconnect":true}`)
		f.expectClosed(conn)
	}
	// Reconnecting with the same signed stream name is turned away now.
	f.subscribe(f.connect(""), identifier, "reject_subscription")
}

func TestSessionGoneByTheTimeTheConnectionIsListeningIsUnauthorized(t *testing.T) {
	f := newFixture(t)
	conn := f.dial("?token=gone")
	f.expect(conn, `{"type":"disconnect","reason":"unauthorized","reconnect":false}`)
	f.expectClosed(conn)
}

func TestDisconnectWithoutReconnectAndServerRestart(t *testing.T) {
	f := newFixture(t)
	banned, other := f.connect(""), f.connect("")
	f.hub.Disconnect(f.owner.ID)
	for _, conn := range []*websocket.Conn{banned, other} {
		f.expect(conn, `{"type":"disconnect","reason":"remote","reconnect":false}`)
		f.expectClosed(conn)
	}

	open := f.connect("")
	go f.hub.Close()
	f.expect(open, `{"type":"disconnect","reason":"server_restart","reconnect":true}`)
	f.expectClosed(open)
}

func TestSubscriptionCommandsFollowTheReference(t *testing.T) {
	f := newFixture(t)
	conn := f.connect("")
	heartbeat := f.identifier("::HeartbeatChannel", map[string]any{})
	f.subscribe(conn, heartbeat, "confirm_subscription")
	// A repeated identifier, an unknown channel and an identifier that isn't an object get no
	// reply at all: the next reply is for the command after them.
	f.send(conn, map[string]string{"command": "subscribe", "identifier": heartbeat})
	f.send(conn, map[string]string{"command": "subscribe", "identifier": f.identifier("NoSuchChannel", map[string]any{})})
	f.send(conn, map[string]string{"command": "subscribe", "identifier": "[]"})
	unreads := f.identifier("UnreadRoomsChannel", map[string]any{})
	f.subscribe(conn, unreads, "confirm_subscription")

	stream := "user_" + jsonInt(f.owner.ID) + "_unreads"
	f.hub.PublishStream(f.ctx, stream, map[string]any{"roomId": f.room.ID})
	f.expect(conn, `{"identifier":`+jsonString(unreads)+`,"message":{"roomId":`+jsonInt(f.room.ID)+`}}`)

	// Once unsubscribed (the reply to the next command shows it's been handled), nothing more
	// arrives from the stream.
	f.send(conn, map[string]string{"command": "unsubscribe", "identifier": unreads})
	f.subscribe(conn, f.identifier("ApplicationCable::Channel", map[string]any{}), "confirm_subscription")
	f.hub.PublishStream(f.ctx, stream, map[string]any{"roomId": -1})
	f.subscribe(conn, unreads, "confirm_subscription")
	f.hub.PublishStream(f.ctx, stream, map[string]any{"roomId": 0})
	f.expect(conn, `{"identifier":`+jsonString(unreads)+`,"message":{"roomId":0}}`)
}

func TestTypingAndPresence(t *testing.T) {
	f := newFixture(t)
	typist, watcher := f.connect(""), f.connect("")
	typingID := f.identifier("TypingNotificationsChannel", map[string]any{"room_id": f.room.ID})
	f.subscribe(typist, typingID, "confirm_subscription")
	f.subscribe(watcher, typingID, "confirm_subscription")
	f.send(typist, map[string]string{"command": "message", "identifier": typingID, "data": `{"action":"start"}`})
	want := `{"identifier":` + jsonString(typingID) + `,"message":{"action":"start","user":{"id":` + jsonInt(f.owner.ID) + `,"name":"Owner"}}}`
	f.expect(typist, want)
	f.expect(watcher, want)

	// Subscribing to presence tells the user's other windows the room has been read.
	reads := f.identifier("ReadRoomsChannel", map[string]any{})
	f.subscribe(watcher, reads, "confirm_subscription")
	f.subscribe(typist, f.identifier("PresenceChannel", map[string]any{"room_id": jsonInt(f.room.ID)}), "confirm_subscription")
	f.expect(watcher, `{"identifier":`+jsonString(reads)+`,"message":{"room_id":`+jsonInt(f.room.ID)+`}}`)
	var connections int
	if err := f.db.Read.QueryRowContext(f.ctx, "SELECT connections FROM memberships WHERE room_id=? AND user_id=?", f.room.ID, f.owner.ID).Scan(&connections); err != nil || connections != 1 {
		t.Fatalf("present: %d connections, %v", connections, err)
	}
}

func TestHeartbeatSendsOneSharedPing(t *testing.T) {
	f := newFixture(t, func(h *Hub) { h.beatEvery = 20 * time.Millisecond })
	a, b := f.dial(""), f.dial("")
	for _, conn := range []*websocket.Conn{a, b} {
		f.expect(conn, `{"type":"welcome"}`)
		_, data, err := conn.Read(f.ctx)
		if err != nil || !pingFrame.Match(data) {
			t.Fatalf("expected a ping, read %s: %v", data, err)
		}
	}
	current := f.hub.beat.Load()
	<-current.next
	if next := f.hub.beat.Load(); next == current || next.frame == current.frame {
		t.Fatal("each beat is one new frame, shared by every connection")
	}
}
