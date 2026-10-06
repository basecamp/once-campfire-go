// Package cable implements the Action Cable room-message transport.
package cable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
)

// fast gates the ENGINE-40 fan-out fast paths for a hub: per-wake batched
// writes (one vectored write per wake instead of one write per frame) and
// the exact-payload frame cache. Set CAMPFIRE_CABLE_FAST=off to select the
// legacy path; default on. Read once per hub at New; tests in this package
// may flip hub.fast directly on a hub they own.
func fastFromEnv() bool {
	return os.Getenv("CAMPFIRE_CABLE_FAST") != "off"
}

// Frame-cache bounds. Bounded FIFO retention: an eviction always drops the
// oldest entries, per key and globally, so a stream alternating between a
// handful of payloads keeps them all (no latest-frame-only thrash).
var (
	frameCachePerKey     = 8       // distinct payloads retained per (scope, identifier)
	frameCacheMaxEntries = 4096    // global entry bound
	frameCacheMaxBytes   = 8 << 20 // global payload-byte bound (plus 64B fixed overhead per entry)
)

type frameCacheKey struct{ scope, id string }

type frameCacheEntry struct {
	key   frameCacheKey
	hash  uint64
	frame *websocket.PreparedMessage

	gPrev, gNext *frameCacheEntry // global FIFO: gPrev toward oldest, gNext toward newest
	kNext        *frameCacheEntry // per-key chain: newest → oldest
	hNext        *frameCacheEntry // hash chain: newest → oldest
}

// frameCache reuses immutable prepared frames across publishes for identical
// (scope, identifier, payload) triples, on top of the prepared message's
// shared compressed bytes. A hit skips re-marshaling and re-deflating the
// payload; reuse is safe because PreparedMessage is immutable (frames may
// also stay queued on slow clients while they are cached).
//
// Poisoning is impossible by construction: equality requires the full
// payload bytes, never the hash alone.
type frameCache struct {
	mu      sync.Mutex
	first   *frameCacheEntry // global FIFO head (oldest)
	last    *frameCacheEntry // global FIFO tail (newest)
	newest  map[frameCacheKey]*frameCacheEntry
	byHash  map[uint64]*frameCacheEntry
	entries int
	bytes   int64
}

func newFrameCache() *frameCache {
	return &frameCache{
		newest: make(map[frameCacheKey]*frameCacheEntry),
		byHash: make(map[uint64]*frameCacheEntry),
	}
}

const (
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
)

// frameHash is FNV-1a 64 over scope, id and payload with separators, so that
// (scope, id, payloadA) and (scope, id, payloadB) never collide in the hash
// map beyond the usual 64-bit probability.
func frameHash(scope, id string, payload []byte) uint64 {
	h := uint64(fnvOffset)
	for i := 0; i < len(scope); i++ {
		h ^= uint64(scope[i])
		h *= fnvPrime
	}
	h ^= 0
	h *= fnvPrime
	for i := 0; i < len(id); i++ {
		h ^= uint64(id[i])
		h *= fnvPrime
	}
	h ^= 0
	h *= fnvPrime
	for _, b := range payload {
		h ^= uint64(b)
		h *= fnvPrime
	}
	return h
}

// lookupOrCreate returns the cached frame whose key and exact payload match,
// creating and inserting one otherwise. Callers must not mutate data.
func (fc *frameCache) lookupOrCreate(scope, id string, data []byte) *websocket.PreparedMessage {
	h := frameHash(scope, id, data)
	key := frameCacheKey{scope: scope, id: id}

	fc.mu.Lock()
	defer fc.mu.Unlock()

	for e := fc.byHash[h]; e != nil; e = e.hNext {
		if e.key == key && bytes.Equal(e.frame.Data(), data) {
			return e.frame
		}
	}

	e := &frameCacheEntry{
		key:   key,
		hash:  h,
		frame: websocket.NewPreparedMessage(websocket.MessageText, data),
	}

	// Insert at the per-key head (newest) and the global tail (newest).
	e.kNext = fc.newest[key]
	fc.newest[key] = e
	e.gPrev = fc.last
	if fc.last != nil {
		fc.last.gNext = e
	} else {
		fc.first = e
	}
	fc.last = e
	e.hNext = fc.byHash[h]
	fc.byHash[h] = e
	fc.entries++
	fc.bytes += int64(len(data)) + 64

	// Per-key FIFO: drop the key's oldest entry once it holds its bound of
	// distinct payloads.
	count := 0
	var oldest *frameCacheEntry
	for n := e; n != nil; n = n.kNext {
		count++
		oldest = n
	}
	if count > frameCachePerKey && oldest != nil && oldest != e {
		fc.remove(oldest)
	}
	// Global FIFO: drop the oldest entry overall while over either bound.
	for fc.entries > frameCacheMaxEntries || fc.bytes > int64(frameCacheMaxBytes) {
		fc.remove(fc.first)
	}
	return e.frame
}

// remove unlinks e from every structure; e must be present.
func (fc *frameCache) remove(e *frameCacheEntry) {
	if e.gPrev != nil {
		e.gPrev.gNext = e.gNext
	} else {
		fc.first = e.gNext
	}
	if e.gNext != nil {
		e.gNext.gPrev = e.gPrev
	} else {
		fc.last = e.gPrev
	}

	prev := (*frameCacheEntry)(nil)
	for n := fc.newest[e.key]; n != nil && n != e; n = n.kNext {
		prev = n
	}
	if prev != nil {
		prev.kNext = e.kNext
	} else if fc.newest[e.key] == e {
		if e.kNext != nil {
			fc.newest[e.key] = e.kNext
		} else {
			delete(fc.newest, e.key)
		}
	}

	prev = nil
	for n := fc.byHash[e.hash]; n != nil && n != e; n = n.hNext {
		prev = n
	}
	if prev != nil {
		prev.hNext = e.hNext
	} else if fc.byHash[e.hash] == e {
		if e.hNext != nil {
			fc.byHash[e.hash] = e.hNext
		} else {
			delete(fc.byHash, e.hash)
		}
	}

	fc.entries--
	fc.bytes -= int64(len(e.frame.Data())) + 64
}

type Hub struct {
	db      *database.DB
	secrets *rails.Secrets
	fast    bool
	mu      sync.RWMutex
	clients map[*client]struct{}
	cache   *frameCache
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
	Channel string
	Room    int64
	Stream  string
	Present bool
}

func New(db *database.DB, secrets *rails.Secrets) *Hub {
	return &Hub{db: db, secrets: secrets, fast: fastFromEnv(), clients: map[*client]struct{}{}, cache: newFrameCache()}
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
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		subs := c.subscriptions
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
		var batch [websocket.BatchMaxFrames]*websocket.PreparedMessage
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
			// ENGINE-40 fast path: coalesce the wake's frame with everything
			// queued (bounded batch) into one write per wake. The select above
			// took the queue's oldest frame, so drain order preserves FIFO.
			// Slow-client policy is untouched: sendFrame still drops into the
			// 256-frame queue and cancels on overflow.
			n := 1
			if frame == nil {
				frame = websocket.NewPreparedMessage(websocket.MessageText, data)
			}
			batch[0] = frame
			if h.fast && !closeAfter {
			drain:
				for n < len(batch) {
					select {
					case f := <-c.out:
						batch[n] = f
						n++
					default:
						break drain
					}
				}
			}
			timeout, stop := context.WithTimeout(ctx, 30*time.Second)
			var err error
			if h.fast {
				err = conn.WritePreparedBatch(timeout, batch[:n])
			} else {
				err = conn.WritePrepared(timeout, batch[0])
			}
			stop()
			if err != nil || closeAfter {
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
						c.subscriptions[command.Identifier] = sub
					}
					h.mu.Unlock()
					c.send(map[string]string{"type": "confirm_subscription", "identifier": command.Identifier})
					if sub.Channel == "PresenceChannel" {
						h.PublishStream(ctx, fmt.Sprintf("user_%d_reads", c.user.ID), map[string]any{"room_id": sub.Room})
					}
					continue
				}
			}
			c.send(map[string]string{"type": "reject_subscription", "identifier": command.Identifier})
		case "unsubscribe":
			h.mu.Lock()
			sub := c.subscriptions[command.Identifier]
			delete(c.subscriptions, command.Identifier)
			h.mu.Unlock()
			if sub.Present {
				h.db.Presence(ctx, c.user.ID, sub.Room, "absent")
			}
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
					h.PublishStream(ctx, sub.Stream, map[string]any{"action": payload.Action, "user": map[string]any{"id": c.user.ID, "name": c.user.Name}})
				}
			case "PresenceChannel":
				action := payload.Action
				if action != "present" && action != "absent" && action != "refresh" {
					continue
				}
				if action == "present" && sub.Present {
					action = "refresh"
				}
				if action == "absent" && !sub.Present {
					continue
				}
				if h.db.Presence(ctx, c.user.ID, sub.Room, action) == nil {
					sub.Present = action != "absent"
					h.mu.Lock()
					c.subscriptions[command.Identifier] = sub
					h.mu.Unlock()
					if payload.Action == "present" {
						h.PublishStream(ctx, fmt.Sprintf("user_%d_reads", c.user.ID), map[string]any{"room_id": sub.Room})
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
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.cancel()
	}
}
func (h *Hub) Publish(ctx context.Context, room int64, markup string) {
	h.publish(ctx, room, "", markup)
}
func (h *Hub) PublishStream(ctx context.Context, name string, message any) {
	h.publish(ctx, 0, name, message)
}
func (h *Hub) publish(ctx context.Context, room int64, name string, message any) {
	type recipient struct {
		client     *client
		identifier string
		room       int64
		scope      string
	}
	var recipients []recipient
	roomScope := ""
	if room != 0 {
		roomScope = fmt.Sprintf("room:%d", room)
	}
	h.mu.RLock()
	for c := range h.clients {
		for identifier, sub := range c.subscriptions {
			if room != 0 && sub.Room == room && sub.Channel == "RoomMessagesChannel" || name != "" && sub.Stream == name {
				scope := name
				if sub.Channel == "RoomMessagesChannel" {
					scope = roomScope
				}
				recipients = append(recipients, recipient{c, identifier, sub.Room, scope})
			}
		}
	}
	h.mu.RUnlock()
	// Recheck every publication; batch distinct sessions rather than trusting a
	// long-lived authorization cache or querying once for every receiving socket.
	groups := make(map[int64]map[string]struct{})
	for _, recipient := range recipients {
		if groups[recipient.room] == nil {
			groups[recipient.room] = make(map[string]struct{})
		}
		groups[recipient.room][recipient.client.token] = struct{}{}
	}
	allowed := make(map[int64]map[string]int64, len(groups))
	for room, tokens := range groups {
		keys := make([]string, 0, len(tokens))
		for token := range tokens {
			keys = append(keys, token)
		}
		var err error
		allowed[room], err = h.db.AuthorizedSessions(ctx, keys, room)
		if err != nil {
			allowed[room] = nil
		}
	}

	// Reuse frames per identifier within this publish; the frame cache
	// extends the reuse across publishes of identical payloads (ENGINE-40).
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
			if h.fast {
				frame = h.cache.lookupOrCreate(r.scope, r.identifier, data)
			} else {
				frame = websocket.NewPreparedMessage(websocket.MessageText, data)
			}
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
