package cable

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
)

// connection is one socket: Serve's goroutine runs the client's commands, and a writer goroutine
// sends the frames queued for it.
type connection struct {
	hub  *Hub
	ws   *websocket.Conn
	user database.User
	wake chan struct{}

	mu      sync.Mutex
	queue   []delivery
	closing *websocket.PreparedMessage // the disconnect frame to finish with, once decided

	// The client's subscriptions by identifier, used by Serve's goroutine only.
	subscriptions map[string]*subscription
}

type delivery struct {
	frame *websocket.PreparedMessage
	from  *subscription // nil for the connection's own frames
}

type subscription struct {
	conn       *connection
	identifier string
	channel    string
	room       int64
	stream     string // the broadcasting it streams from, if any
	present    bool   // PresenceChannel: the membership is counted as connected

	group *group // guarded by Hub.mu
	index int    // position in group.members, guarded by Hub.mu

	pending int // frames queued but not yet written, guarded by conn.mu
}

// Serve upgrades the request to an Action Cable connection for user, signed in with the session
// token, and runs it until it closes.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, user database.User, token string) {
	h.startHeartbeat()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"actioncable-v1-json"}, CompressionMode: websocket.CompressionNoContextTakeover, CompressionThreshold: 256})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	if ws.Subprotocol() != "actioncable-v1-json" {
		ws.Close(websocket.StatusPolicyViolation, "unsupported protocol")
		return
	}
	ws.SetReadLimit(1 << 20)
	c := &connection{hub: h, ws: ws, user: user, wake: make(chan struct{}, 1), subscriptions: map[string]*subscription{}}
	if !h.register(c) {
		return
	}
	defer h.unregister(c)
	done, written := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(written)
		c.write(done)
	}()
	defer func() {
		close(done)
		ws.CloseNow()
		<-written
	}()

	ctx := r.Context()
	// A ban or sign-out that disconnected this user before the connection was registered went
	// unheard, so check the session again now that a disconnect would reach it.
	if _, err := h.db.SessionUser(ctx, token); err != nil {
		c.close(unauthorized)
		<-written
		return
	}
	c.transmit(welcome)
	for {
		kind, data, err := ws.Read(context.Background())
		if err != nil {
			return
		}
		if kind == websocket.MessageText {
			c.dispatch(ctx, data)
		}
	}
}

// dispatch is Subscriptions#execute_command. Malformed commands are ignored.
func (c *connection) dispatch(ctx context.Context, data []byte) {
	var command struct{ Command, Identifier, Data string }
	if json.Unmarshal(data, &command) != nil {
		return
	}
	switch command.Command {
	case "subscribe":
		c.subscribe(ctx, command.Identifier)
	case "unsubscribe":
		c.unsubscribe(ctx, command.Identifier)
	case "message":
		c.perform(ctx, command.Identifier, command.Data)
	}
}

// subscribe is Subscriptions#add. A repeated identifier is ignored, as is one beyond the limits
// or naming no channel; otherwise the channel confirms or rejects the subscription.
func (c *connection) subscribe(ctx context.Context, identifier string) {
	if _, exists := c.subscriptions[identifier]; exists || len(c.subscriptions) >= maxSubscriptions || len(identifier) > maxIdentifier {
		return
	}
	s, known := c.hub.authorize(ctx, c.user.ID, identifier)
	if !known {
		return
	}
	if s != nil && s.channel == "PresenceChannel" {
		if c.hub.db.Presence(ctx, c.user.ID, s.room, "present") != nil {
			s = nil
		} else {
			s.present = true
		}
	}
	if s == nil {
		c.transmit(frame(rejection(identifier)))
		return
	}
	s.conn = c
	c.subscriptions[identifier] = s
	// Confirmed before the stream starts, so its frames follow the confirmation.
	c.transmit(frame(confirmation(identifier)))
	c.hub.join(s)
	if s.channel == "PresenceChannel" {
		c.hub.broadcast(readRooms(c.user.ID), readRoom(s.room))
	}
}

// unsubscribe is Subscriptions#remove: no reply either way.
func (c *connection) unsubscribe(ctx context.Context, identifier string) {
	s := c.subscriptions[identifier]
	if s == nil {
		return
	}
	delete(c.subscriptions, identifier)
	c.hub.leave(s)
	if s.present {
		c.hub.db.Presence(ctx, c.user.ID, s.room, "absent")
	}
}

// perform is Subscriptions#perform_action for the channels with actions.
func (c *connection) perform(ctx context.Context, identifier, data string) {
	s := c.subscriptions[identifier]
	if s == nil {
		return
	}
	var payload struct{ Action string }
	if json.Unmarshal([]byte(data), &payload) != nil {
		return
	}
	switch s.channel {
	case "TypingNotificationsChannel":
		if payload.Action == "start" || payload.Action == "stop" {
			c.hub.broadcast(s.stream, typing(payload.Action, c.user))
		}
	case "PresenceChannel":
		action := payload.Action
		if action != "present" && action != "absent" && action != "refresh" {
			return
		}
		if action == "present" && s.present {
			action = "refresh"
		}
		if action == "absent" && !s.present {
			return
		}
		// The reference finds the membership to update it, and fails without one.
		if _, err := c.hub.db.Room(ctx, c.user.ID, s.room); err != nil {
			return
		}
		if c.hub.db.Presence(ctx, c.user.ID, s.room, action) == nil {
			s.present = action != "absent"
			if payload.Action == "present" {
				c.hub.broadcast(readRooms(c.user.ID), readRoom(s.room))
			}
		}
	}
}

// authorize is a channel's subscribed callback: the subscription when the user may have it, nil
// when the channel rejects it, and known false for an identifier that names no channel.
func (h *Hub) authorize(ctx context.Context, user int64, identifier string) (_ *subscription, known bool) {
	var params struct {
		Channel string
		Signed  string          `json:"signed_stream_name"`
		Room    json.RawMessage `json:"room_id"`
	}
	if json.Unmarshal([]byte(identifier), &params) != nil {
		return nil, false
	}
	// safe_constantize resolves "::RoomChannel" to RoomChannel too.
	s := &subscription{identifier: identifier, channel: strings.TrimPrefix(params.Channel, "::")}
	switch s.channel {
	case "ApplicationCable::Channel", "HeartbeatChannel":
		return s, true
	case "ReadRoomsChannel":
		s.stream = readRooms(user)
		return s, true
	case "UnreadRoomsChannel":
		s.stream = unreadRooms(user)
		return s, true
	case "RoomMessagesChannel":
		name, err := h.secrets.VerifyStream(params.Signed)
		if err != nil {
			return nil, true
		}
		kind, id, err := rails.StreamRoom(name)
		if err != nil {
			return nil, true
		}
		actual, err := h.db.Room(ctx, user, id)
		if err != nil || kind != "Room" && kind != actual.Type {
			return nil, true
		}
		s.room, s.stream = id, roomMessages(id)
		return s, true
	case "RoomChannel", "PresenceChannel", "TypingNotificationsChannel":
		raw := string(params.Room)
		var str string
		if json.Unmarshal(params.Room, &str) == nil {
			raw = str
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, true
		}
		if _, err = h.db.Room(ctx, user, id); err != nil {
			return nil, true
		}
		s.room, s.stream = id, fmt.Sprintf("%s:%d", s.channel, id)
		return s, true
	case "Turbo::StreamsChannel":
		name, err := h.secrets.VerifyStream(params.Signed)
		if err != nil {
			return nil, true
		}
		// RoomStreamsAreAuthorized: room message streams are RoomMessagesChannel's alone.
		if _, suffix, ok := strings.Cut(name, ":"); ok && suffix == "messages" {
			return nil, true
		}
		s.stream = name
		return s, true
	}
	return nil, false
}

// transmit queues a frame of the connection's own (welcome, a subscription's confirmation)
// behind what's already queued.
func (c *connection) transmit(frame *websocket.PreparedMessage) { c.enqueue(nil, frame) }

// enqueue queues a frame for the writer. A subscription that falls streamCapacity frames behind
// closes the connection with reconnect: true instead (the reference's lagging subscriber).
func (c *connection) enqueue(from *subscription, frame *websocket.PreparedMessage) {
	c.mu.Lock()
	switch {
	case c.closing != nil:
	case from != nil && from.pending == streamCapacity:
		c.closing = lagged
	default:
		if from != nil {
			from.pending++
		}
		c.queue = append(c.queue, delivery{frame, from})
	}
	c.mu.Unlock()
	c.signal()
}

// close ends the connection with frame, a disconnect message, once what's being written is out.
func (c *connection) close(frame *websocket.PreparedMessage) {
	c.mu.Lock()
	if c.closing == nil {
		c.closing = frame
	}
	c.mu.Unlock()
	c.signal()
}

func (c *connection) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// write sends the connection's frames until done is closed: what's queued, up to maxWriteBatch
// frames per socket write, and the heartbeat's shared ping. Once the connection is closing it
// sends the disconnect frame and closes the socket.
func (c *connection) write(done <-chan struct{}) {
	beat := c.hub.beat.Load()
	batch := make([]*websocket.PreparedMessage, 0, maxWriteBatch+2)
	for {
		select {
		case <-done:
			return
		case <-c.wake:
		case <-beat.next:
			beat = c.hub.beat.Load()
			batch = append(batch, beat.frame)
		}
		var closing *websocket.PreparedMessage
		batch, closing = c.take(batch)
		if closing != nil {
			batch = append(batch, closing)
		}
		if err := c.ws.WritePreparedBatch(time.Now().Add(writeTimeout), batch); err != nil {
			c.ws.CloseNow()
			return
		}
		clear(batch)
		batch = batch[:0]
		if closing != nil {
			c.ws.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}

// take moves up to maxWriteBatch queued frames into batch, and returns the disconnect frame if
// the connection is closing.
func (c *connection) take(batch []*websocket.PreparedMessage) ([]*websocket.PreparedMessage, *websocket.PreparedMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := min(len(c.queue), maxWriteBatch)
	for _, d := range c.queue[:n] {
		batch = append(batch, d.frame)
		if d.from != nil {
			d.from.pending--
		}
	}
	rest := copy(c.queue, c.queue[n:])
	clear(c.queue[rest:])
	c.queue = c.queue[:rest]
	switch {
	case rest > 0:
		c.signal()
	case cap(c.queue) > 4*maxWriteBatch:
		// Don't keep a burst's worth of queue on an idle connection.
		c.queue = nil
	}
	return batch, c.closing
}
