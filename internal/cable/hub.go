// Package cable implements the Action Cable room-message transport.
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

type Hub struct {
	db          *database.DB
	secrets     *rails.Secrets
	mu          sync.RWMutex
	closed      bool
	serving     sync.WaitGroup
	clients     map[*client]struct{}
	subscribers map[publication][]recipient
}
type client struct {
	disconnect    chan bool
	user          database.User
	token         string
	cancel        context.CancelFunc
	out           chan *websocket.PreparedMessage
	subscriptions map[string]subscription
}

type subscription struct {
	Channel  string
	Room     int64
	Stream   string
	Present  bool
	position int
}

type publication struct {
	room   int64
	stream string
}
type recipient struct {
	client     *client
	identifier string
	room       int64
}

func destination(sub subscription) publication {
	if sub.Channel == "RoomMessagesChannel" {
		return publication{room: sub.Room}
	}
	return publication{stream: sub.Stream}
}
func New(db *database.DB, secrets *rails.Secrets) *Hub {
	return &Hub{db: db, secrets: secrets, clients: map[*client]struct{}{}, subscribers: make(map[publication][]recipient)}
}

// Subscription state and its routing index have one owner under h.mu. The index
// only selects candidates; session and membership checks remain publication-local.
func (h *Hub) setSubscription(c *client, identifier string, sub subscription) {
	if old, exists := c.subscriptions[identifier]; exists {
		if destination(old) == destination(sub) && old.Room == sub.Room {
			sub.position = old.position
			c.subscriptions[identifier] = sub
			return
		}
		h.unindex(old)
	}
	key := destination(sub)
	sub.position = -1
	if key != (publication{}) {
		bucket := h.subscribers[key]
		sub.position = len(bucket)
		h.subscribers[key] = append(bucket, recipient{c, identifier, sub.Room})
	}
	c.subscriptions[identifier] = sub
}

// Dense routing buckets make publication a contiguous O(recipients) copy;
// each subscription's inverse position makes removal O(1) by swapping the tail.
func (h *Hub) unindex(sub subscription) {
	key := destination(sub)
	if key == (publication{}) {
		return
	}
	bucket := h.subscribers[key]
	last := len(bucket) - 1
	moved := bucket[last]
	bucket[sub.position] = moved
	bucket[last] = recipient{} // Do not retain disconnected sockets in spare capacity.
	bucket = bucket[:last]
	if sub.position != last {
		other := moved.client.subscriptions[moved.identifier]
		other.position = sub.position
		moved.client.subscriptions[moved.identifier] = other
	}
	if len(bucket) == 0 {
		delete(h.subscribers, key)
	} else {
		h.subscribers[key] = bucket
	}
}
func (c *client) send(value any) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return c.sendFrame(websocket.NewPreparedMessage(websocket.MessageText, data))
}
func (c *client) sendFrame(data *websocket.PreparedMessage) bool {
	select {
	case c.out <- data:
		return true
	default:
		c.cancel()
		return false
	}
}
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, user database.User, token string) {
	// Admission and the join count share the close lock, including upgrades that
	// have not yet installed their client. No positive Add can race a zero Wait.
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	h.serving.Add(1)
	h.mu.Unlock()
	defer h.serving.Done()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"actioncable-v1-json"}, CompressionMode: websocket.CompressionNoContextTakeover, CompressionThreshold: 256})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != "actioncable-v1-json" {
		conn.Close(websocket.StatusPolicyViolation, "unsupported protocol")
		return
	}
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &client{disconnect: make(chan bool, 1), user: user, token: token, cancel: cancel, out: make(chan *websocket.PreparedMessage, 256), subscriptions: map[string]subscription{}}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		subs := c.subscriptions
		for identifier := range subs {
			h.unindex(subs[identifier])
		}
		h.mu.Unlock()
		for _, sub := range subs {
			if sub.Present {
				ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
				h.db.Presence(ctx, c.user.ID, sub.Room, "absent")
				stop()
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		writer := newFrameWriter(ctx, cancel, conn, 30*time.Second)
		defer writer.Close()
		for {
			var data []byte
			var frame *websocket.PreparedMessage
			closeAfter := false
			select {
			case <-ctx.Done():
				return
			case reconnect := <-c.disconnect:
				data, _ = json.Marshal(map[string]any{"type": "disconnect", "reason": "remote", "reconnect": reconnect})
				closeAfter = true
			case frame = <-c.out:
			case <-ticker.C:
				if _, err := h.db.SessionUser(ctx, c.token); err != nil {
					return
				}
				data, _ = json.Marshal(map[string]any{"type": "ping", "message": time.Now().Unix()})
			}
			if frame == nil {
				frame = websocket.NewPreparedMessage(websocket.MessageText, data)
			}
			if err := writer.Write(frame); err != nil || closeAfter {
				return
			}
		}
	}()
	defer func() { cancel(); <-done }()
	c.send(map[string]string{"type": "welcome"})
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText {
			conn.Close(websocket.StatusUnsupportedData, "text commands required")
			break
		}
		var command struct{ Command, Identifier, Data string }
		if json.Unmarshal(data, &command) != nil || len(command.Identifier) > 4096 {
			continue
		}
		switch command.Command {
		case "subscribe":
			sub, valid := h.subscription(ctx, c, command.Identifier)
			h.mu.Lock()
			_, exists := c.subscriptions[command.Identifier]
			available := exists || len(c.subscriptions) < 64
			h.mu.Unlock()
			if valid && available {
				if !exists && sub.Channel == "PresenceChannel" {
					if h.db.Presence(ctx, c.user.ID, sub.Room, "present") != nil {
						valid = false
					} else {
						sub.Present = true
					}
				}
				if valid {
					h.mu.Lock()
					if !exists {
						h.setSubscription(c, command.Identifier, sub)
					}
					h.mu.Unlock()
					c.send(map[string]string{"type": "confirm_subscription", "identifier": command.Identifier})
					if sub.Channel == "PresenceChannel" {
						h.PublishStreams(ctx, map[string]any{"room_id": sub.Room}, fmt.Sprintf("user_%d_reads", c.user.ID))
					}
					continue
				}
			}
			c.send(map[string]string{"type": "reject_subscription", "identifier": command.Identifier})
		case "unsubscribe":
			h.mu.RLock()
			sub, exists := c.subscriptions[command.Identifier]
			h.mu.RUnlock()
			if !exists {
				continue
			}
			// Keep presence owned until its decrement commits. On failure,
			// disconnect cleanup retains the cancellation-independent retry.
			if sub.Present && h.db.Presence(ctx, c.user.ID, sub.Room, "absent") != nil {
				return
			}
			h.mu.Lock()
			h.unindex(c.subscriptions[command.Identifier])
			delete(c.subscriptions, command.Identifier)
			h.mu.Unlock()
		case "message":
			h.mu.RLock()
			sub, exists := c.subscriptions[command.Identifier]
			h.mu.RUnlock()
			if !exists {
				continue
			}
			var payload struct{ Action string }
			if json.Unmarshal([]byte(command.Data), &payload) != nil {
				continue
			}
			if _, err := h.db.SessionUser(ctx, c.token); err != nil {
				return
			}
			if sub.Room != 0 {
				if _, err := h.db.Room(ctx, c.user.ID, sub.Room); err != nil {
					continue
				}
			}
			switch sub.Channel {
			case "TypingNotificationsChannel":
				if payload.Action == "start" || payload.Action == "stop" {
					h.PublishStreams(ctx, map[string]any{"action": payload.Action, "user": map[string]any{"id": c.user.ID, "name": c.user.Name}}, sub.Stream)
				}
			case "PresenceChannel":
				action := payload.Action
				if action != "present" && action != "absent" && action != "refresh" {
					continue
				}
				if action == "present" && sub.Present {
					action = "read"
				}
				if (action == "absent" || action == "refresh") && !sub.Present {
					continue
				}
				if h.db.Presence(ctx, c.user.ID, sub.Room, action) == nil {
					sub.Present = action != "absent"
					h.mu.Lock()
					h.setSubscription(c, command.Identifier, sub)
					h.mu.Unlock()
					if payload.Action == "present" {
						h.PublishStreams(ctx, map[string]any{"room_id": sub.Room}, fmt.Sprintf("user_%d_reads", c.user.ID))
					}
				}
			}

		}
	}
}
func (h *Hub) Disconnect(user int64) { h.disconnect(user, false) }
func (h *Hub) Reconnect(user int64)  { h.disconnect(user, true) }
func (h *Hub) disconnect(user int64, reconnect bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.user.ID == user {
			select {
			case c.disconnect <- reconnect:
			default:
				c.cancel()
			}
		}
	}
}
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	for c := range h.clients {
		c.cancel()
	}
	h.mu.Unlock()
	// Serve returns only after its writer, persisted absence and socket close.
	h.serving.Wait()
}
func (h *Hub) Publish(ctx context.Context, room int64, markup string) {
	h.publish(ctx, markup, publication{room: room})
}

// PublishStreams publishes one event to its selected streams. Each distinct
// session is authorized afresh; frames are shared only for identical identifiers.
func (h *Hub) PublishStreams(ctx context.Context, message any, names ...string) {
	keys := make([]publication, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := seen[name]; !exists {
			seen[name] = struct{}{}
			keys = append(keys, publication{stream: name})
		}
	}
	h.publish(ctx, message, keys...)
}

func (h *Hub) publish(ctx context.Context, message any, keys ...publication) {
	var recipients []recipient
	h.mu.RLock()
	for _, key := range keys {
		if key != (publication{}) {
			recipients = append(recipients, h.subscribers[key]...)
		}
	}
	h.mu.RUnlock()
	// Recheck every publication; batch distinct sessions rather than trusting a
	// long-lived authorization cache or querying once for every receiving socket.
	allowed := make(map[int64]map[string]int64)
	for _, recipient := range recipients {
		if allowed[recipient.room] == nil {
			allowed[recipient.room] = make(map[string]int64)
		}
		allowed[recipient.room][recipient.client.token] = 0
	}
	for room, sessions := range allowed {
		if err := h.db.AuthorizeSessions(ctx, sessions, room); err != nil {
			allowed[room] = nil
		}
	}

	frames := make(map[string]*websocket.PreparedMessage)
	for _, r := range recipients {
		if allowed[r.room][r.client.token] != r.client.user.ID {
			r.client.cancel()
			continue
		}

		frame, exists := frames[r.identifier]
		if !exists {
			data, err := json.Marshal(struct {
				Identifier string `json:"identifier"`
				Message    any    `json:"message"`
			}{r.identifier, message})
			if err != nil {
				return
			}
			frame = websocket.NewPreparedMessage(websocket.MessageText, data)
			frames[r.identifier] = frame
		}
		r.client.sendFrame(frame)
	}

}

func (h *Hub) subscription(ctx context.Context, c *client, identifier string) (subscription, bool) {
	var params struct {
		Channel string
		Signed  string          `json:"signed_stream_name"`
		Room    json.RawMessage `json:"room_id"`
	}
	if json.Unmarshal([]byte(identifier), &params) != nil {
		return subscription{}, false
	}
	sub := subscription{Channel: params.Channel}
	switch params.Channel {
	case "ApplicationCable::Channel", "HeartbeatChannel":
		return sub, true
	case "ReadRoomsChannel":
		sub.Stream = fmt.Sprintf("user_%d_reads", c.user.ID)
		return sub, true
	case "UnreadRoomsChannel":
		sub.Stream = fmt.Sprintf("user_%d_unreads", c.user.ID)
		return sub, true
	case "RoomMessagesChannel":
		name, err := h.secrets.VerifyStream(params.Signed)
		if err != nil {
			return sub, false
		}
		kind, id, err := rails.StreamRoom(name)
		if err != nil {
			return sub, false
		}
		actual, err := h.db.Room(ctx, c.user.ID, id)
		if err != nil || kind != "Room" && kind != actual.Type {
			return sub, false
		}
		sub.Room = id
		return sub, true
	case "RoomChannel", "PresenceChannel", "TypingNotificationsChannel":
		raw := string(params.Room)
		var str string
		if json.Unmarshal(params.Room, &str) == nil {
			raw = str
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return sub, false
		}
		if _, err = h.db.Room(ctx, c.user.ID, id); err != nil {
			return sub, false
		}
		sub.Room = id
		sub.Stream = fmt.Sprintf("%s:%d", params.Channel, id)
		return sub, true
	case "Turbo::StreamsChannel":
		name, err := h.secrets.VerifyStream(params.Signed)
		if err != nil {
			return sub, false
		}
		if _, suffix, ok := strings.Cut(name, ":"); ok && suffix == "messages" {
			return sub, false
		}
		sub.Stream = name
		return sub, true
	}
	return sub, false
}
