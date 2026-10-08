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
	"sync/atomic"
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

// Write bounds for the hub's per-wake batch write. cableWriteDeadline bounds
// one stalled write; cableDeadlineRearm is how much budget an armed socket
// deadline must still have before a write can use it without re-arming (see
// the writer loop in Serve).
const (
	cableWriteDeadline = 30 * time.Second
	cableDeadlineRearm = cableWriteDeadline / 2
)

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
	// heartbeat is the session-check/ping interval per connection; the wire
	// behaviour at the default 3s is unchanged (ENGINE-54 keeps the check
	// but gates it on the session generations). Tests shorten it.
	heartbeat time.Duration
	mu        sync.RWMutex
	clients   map[*client]struct{}
	// roomStreams / namedStreams are the publish recipient index (ENGINE-61):
	// subscribers keyed by RoomMessagesChannel room, respectively by exact
	// stream name, each entry listing the client's matching subscription
	// identifiers. publish() snapshots these instead of scanning every
	// client's full subscription map (6 subscriptions × 1000 clients per
	// publish before; one list of the actual recipients after). The index is
	// maintained under mu next to clients/subscriptions, so a publish's
	// snapshot is consistent with the client set, and the matching rules are
	// literally the subscribe-time mirror of publish()'s old match.
	roomStreams  map[int64]map[*client][]string
	namedStreams map[string]map[*client][]string
	cache        *frameCache
	authz        *authzCache
}
type client struct {
	user          database.User
	token         string
	cancel        context.CancelFunc
	q             *outQueue
	subscriptions map[string]subscription
	// dying is set by stop() when the connection is torn down; the writer
	// reads it before parking and after each wake. discPending/discReconnect
	// carry a Disconnect request to the writer (see requestDisconnect).
	dying         atomic.Bool
	discPending   atomic.Bool
	discReconnect atomic.Bool
	// sessVer/userVer are the (SessionVersion, UserVersion) generations the
	// last heartbeat session check ran under; an unchanged pair skips the
	// re-check (ENGINE-54).
	sessVer int64
	userVer int64
}

// outQueueCap is the slow-client bound: a client whose undelivered frames
// reach this many is dropped, exactly the historical 256-frame channel
// policy.
const outQueueCap = 256

// outQueue is the per-client delivery queue (ENGINE-61). The hub's publish
// loop appends into a fixed ring under one mutex and signals the parked
// writer once per wake; the writer pops up to BatchMaxFrames per wake under
// a single lock acquisition. The channel it replaced cost a mutex pair plus
// a ring-buffer copy per send on the publisher and a per-frame receive on
// the writer; with ~500k client deliveries per second at 1000 clients those
// were the dominant per-delivery costs after the write itself. The queue
// bound, ordering, and overflow-stop are unchanged.
type outQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	frames []*websocket.PreparedMessage // ring, len(outQueueCap) for live clients
	head   int
	len    int
	parked bool
}

func newOutQueue() *outQueue { return newOutQueueCap(outQueueCap) }

func newOutQueueCap(capacity int) *outQueue {
	q := &outQueue{frames: make([]*websocket.PreparedMessage, capacity)}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// send appends one frame, stopping the client when the ring is full (the
// 256-frame slow-client policy). It returns whether the frame was queued.
// The writer is signalled only when it is parked, so a running writer (or
// one about to drain what it already saw) is not woken again.
func (q *outQueue) send(c *client, data *websocket.PreparedMessage) bool {
	q.mu.Lock()
	if q.len == len(q.frames) {
		q.mu.Unlock()
		c.stop()
		return false
	}
	q.frames[(q.head+q.len)%len(q.frames)] = data
	q.len++
	wake := q.parked
	q.mu.Unlock()
	if wake {
		q.cond.Signal()
	}
	return true
}

// register adds a client and its existing subscriptions to the hub and the
// recipient index under one lock; used by tests with simulated clients and by
// nothing in the production path (Serve maintains the index incrementally).
func (h *Hub) register(c *client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	for identifier, sub := range c.subscriptions {
		h.indexAddLocked(c, identifier, sub)
	}
	h.mu.Unlock()
}

// indexAddLocked mirrors one subscription into the publish index; the caller
// holds h.mu (write).
func (h *Hub) indexAddLocked(c *client, identifier string, sub subscription) {
	if sub.Channel == "RoomMessagesChannel" && sub.Room != 0 {
		m := h.roomStreams[sub.Room]
		if m == nil {
			m = make(map[*client][]string)
			h.roomStreams[sub.Room] = m
		}
		m[c] = append(m[c], identifier)
	}
	if sub.Stream != "" {
		m := h.namedStreams[sub.Stream]
		if m == nil {
			m = make(map[*client][]string)
			h.namedStreams[sub.Stream] = m
		}
		m[c] = append(m[c], identifier)
	}
}

// indexRemoveLocked drops one subscription from the publish index; the
// caller holds h.mu (write).
func (h *Hub) indexRemoveLocked(c *client, identifier string, sub subscription) {
	if sub.Channel == "RoomMessagesChannel" && sub.Room != 0 {
		if m := h.roomStreams[sub.Room]; m != nil {
			m[c] = removeIdentifier(m[c], identifier)
			if len(m[c]) == 0 {
				delete(m, c)
				if len(m) == 0 {
					delete(h.roomStreams, sub.Room)
				}
			}
		}
	}
	if sub.Stream != "" {
		if m := h.namedStreams[sub.Stream]; m != nil {
			m[c] = removeIdentifier(m[c], identifier)
			if len(m[c]) == 0 {
				delete(m, c)
				if len(m) == 0 {
					delete(h.namedStreams, sub.Stream)
				}
			}
		}
	}
}

// removeIdentifier drops id from ids, leaving the slice order of the rest.
func removeIdentifier(ids []string, id string) []string {
	for i, x := range ids {
		if x == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

// clear drops every queued frame; used only by tests.
func (q *outQueue) clear() {
	q.mu.Lock()
	q.head, q.len = 0, 0
	q.mu.Unlock()
}

type subscription struct {
	Channel string
	Room    int64
	Stream  string
	Present bool
}

func New(db *database.DB, secrets *rails.Secrets) *Hub {
	return &Hub{db: db, secrets: secrets, fast: fastFromEnv(), heartbeat: 3 * time.Second, clients: map[*client]struct{}{}, roomStreams: map[int64]map[*client][]string{}, namedStreams: map[string]map[*client][]string{}, cache: newFrameCache(), authz: newAuthzCache()}
}
func (c *client) send(value any) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return c.sendFrame(websocket.NewPreparedMessage(websocket.MessageText, data))
}
func (c *client) sendFrame(data *websocket.PreparedMessage) bool {
	return c.q.send(c, data)
}

// stop tears the connection down: the writer returns without writing any
// queued frame, and the request context cancels so the read loop exits too.
// It is idempotent and safe to call from any goroutine. The cond signal
// wakes a writer parked on an empty queue; a running writer sees dying at
// its next loop turn instead.
func (c *client) stop() {
	if c.dying.CompareAndSwap(false, true) {
		c.cancel()
		c.q.mu.Lock()
		c.q.cond.Signal()
		c.q.mu.Unlock()
	}
}

// requestDisconnect asks the writer to send the Action Cable disconnect frame
// and then exit, exactly the former c.disconnect channel handoff. A second
// request while one is pending (or while stopping) tears down immediately,
// like the former buffered-channel default.
func (c *client) requestDisconnect(reconnect bool) {
	if c.dying.Load() || !c.discPending.CompareAndSwap(false, true) {
		c.stop()
		return
	}
	c.discReconnect.Store(reconnect)
	c.q.mu.Lock()
	c.q.cond.Signal()
	c.q.mu.Unlock()
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
	c := &client{user: user, token: token, cancel: cancel, q: newOutQueue(), subscriptions: map[string]subscription{}}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		for identifier, sub := range c.subscriptions {
			h.indexRemoveLocked(c, identifier, sub)
		}
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
		var batch [websocket.BatchMaxFrames]*websocket.PreparedMessage
		q := c.q
		// deadlineArmedAt is when the socket write deadline last moved, local
		// to this writer. See the write below and cableDeadlineRearm.
		var deadlineArmedAt time.Time
		writeBatch := func(n int) error {
			if h.fast {
				// ENGINE-54: bound the wake's write with an absolute socket
				// deadline instead of a per-wake context. The context armed
				// and stopped two runtime timers and allocated per wake (the
				// write's own WithTimeout plus the connection's AfterFunc),
				// the dominant per-wake cost at 1000 clients. ENGINE-61: the
				// deadline is armed only when it no longer has at least
				// cableDeadlineRearm of budget left, and it REMAINS armed
				// between batches: WritePreparedBatchDeadlineRetained
				// installs the socket deadline and never clears it, so the
				// unarmed branch below still writes under the remaining
				// budget. (The batch API once cleared the deadline after
				// every write, silently breaking that invariant and leaving
				// every unarmed batch unbounded; the retained variant
				// restores it.) Arming costs a netpoll timer lock plus a
				// timer modify, and clearing costs the same again; a
				// wake-with-frames at 1000 clients arrives every couple of
				// milliseconds, and both calls were pure overhead on the
				// ~500k-writes-per-second path. Re-arming every
				// cableDeadlineRearm still bounds every stalled write at <=
				// cableWriteDeadline: the unarmed branch is only taken while
				// the armed deadline is at least cableDeadlineRearm in the
				// future, so no write can start against an expired or
				// about-to-expire deadline, and once the deadline passes the
				// next batch re-arms before writing. The deadline is
				// absolute on the socket, so it also bounds the heartbeat's
				// pings, whose context path reaps by closing the connection
				// and never clears a socket deadline. The legacy path below
				// keeps the historical per-write context behaviour.
				now := time.Now()
				if deadlineArmedAt.IsZero() || now.Sub(deadlineArmedAt) >= cableDeadlineRearm {
					err := conn.WritePreparedBatchDeadlineRetained(now.Add(cableWriteDeadline), batch[:n])
					deadlineArmedAt = now
					return err
				}
				return conn.WritePreparedBatch(context.Background(), batch[:n])
			}
			timeout, stop := context.WithTimeout(ctx, cableWriteDeadline)
			err := conn.WritePrepared(timeout, batch[0])
			stop()
			return err
		}
		for {
			// Park until a frame is queued or a terminal request lands. The
			// request flags are checked before parking, so a stop or
			// disconnect is honoured even while the queue stays full and the
			// writer keeps draining: no wakeup can be lost.
			q.mu.Lock()
			for q.len == 0 && !c.dying.Load() && !c.discPending.Load() {
				q.parked = true
				q.cond.Wait()
			}
			q.parked = false
			if c.dying.Load() {
				q.mu.Unlock()
				return
			}
			if c.discPending.Load() {
				q.mu.Unlock()
				data, _ := json.Marshal(map[string]any{"type": "disconnect", "reason": "remote", "reconnect": c.discReconnect.Load()})
				batch[0] = websocket.NewPreparedMessage(websocket.MessageText, data)
				writeBatch(1)
				return
			}
			// ENGINE-40 fast path: pop what is queued (bounded batch) under
			// one lock acquisition and write it in one vectored write per
			// wake. The ring preserves FIFO. Slow-client policy is
			// untouched: sendFrame still drops into the 256-frame queue and
			// stops the client on overflow. The legacy path keeps its
			// one-frame-per-wake behaviour.
			bound := 1
			if h.fast {
				bound = websocket.BatchMaxFrames
			}
			n := min(q.len, bound)
			for i := 0; i < n; i++ {
				batch[i] = q.frames[(q.head+i)%len(q.frames)]
			}
			q.head = (q.head + n) % len(q.frames)
			q.len -= n
			q.mu.Unlock()
			if err := writeBatch(n); err != nil {
				return
			}
		}
	}()
	// The heartbeat owns the session re-check and the ping; it runs on its
	// own goroutine so the writer parks on the frame queue alone (ENGINE-61:
	// the four-case select per wake was the largest receiver-side cost). The
	// ping is written directly rather than queued so it never counts against
	// the 256-frame slow-client bound, exactly as before.
	go func() {
		ticker := time.NewTicker(h.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// ENGINE-54: the session check runs only when a sessions-row
				// or users.status write committed since the last check (the
				// versioned generations, like the ENGINE-40b authorization
				// cache). At 1000 clients an un-gated tick is ~330 SQLite
				// queries per second re-verifying sessions that cannot have
				// changed; a session insert/delete or a user ban/deactivation
				// bumps a generation, so the skip is exact under the
				// documented single-process write model. The ping itself is
				// still sent every tick.
				if sv, uv := h.db.SessionVersion(), h.db.UserVersion(); sv != c.sessVer || uv != c.userVer {
					if _, err := h.db.SessionUser(ctx, c.token); err != nil {
						c.stop()
						return
					}
					c.sessVer, c.userVer = sv, uv
				}
				data, _ := json.Marshal(map[string]any{"type": "ping", "message": time.Now().Unix()})
				frame := websocket.NewPreparedMessage(websocket.MessageText, data)
				// A single prepared write, not a batch: the batch path reuses
				// per-connection scratch before taking the frame lock, so it
				// is reserved for the writer goroutine. The context bound
				// keeps a stalled ping from holding this goroutine forever.
				timeout, stop := context.WithTimeout(ctx, cableWriteDeadline)
				err := conn.WritePrepared(timeout, frame)
				stop()
				if err != nil {
					c.stop()
					return
				}
			}
		}
	}()
	defer func() { c.stop(); <-done }()
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
						h.indexAddLocked(c, command.Identifier, sub)
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
			h.indexRemoveLocked(c, command.Identifier, sub)
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
			c.requestDisconnect(reconnect)
		}
	}
}
func (h *Hub) Close() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.stop()
	}
}
func (h *Hub) Publish(ctx context.Context, room int64, markup string) {
	h.publish(ctx, room, "", markup)
}
func (h *Hub) PublishStream(ctx context.Context, name string, message any) {
	h.publish(ctx, 0, name, message)
}

// recipient is one delivery target of a publish: the client, the subscription
// identifier of the matching subscription, and the authorization scope. user
// is the authorized session user for the (room, token) pair, filled by the
// authorization pass; 0 means unauthorized (the client is cancelled).
type recipient struct {
	client     *client
	identifier string
	room       int64
	scope      string
	user       int64
}

// authzKey identifies one publication authorization: a session token in a
// room (room 0 = stream-only publishes, which skip the membership check).
type authzKey struct {
	room  int64
	token string
}

// authzEntry caches the result of AuthorizedSessions for one (room, token)
// pair plus the (session, membership, user-status) generations it was
// computed under. An entry never outlives its generations: a lookup serves
// it only while all three counters still equal the stored values, so a
// revoked, banned or removed client is excluded from the next publication
// after the audited write helper returns (each helper bumps after commit).
type authzEntry struct {
	userID      int64 // authorized session user; 0 = cached negative result
	sess        int64
	member      int64
	userVersion int64
}

const (
	// authzCacheMax bounds the authorization cache. Generation mismatch
	// ordinarily invalidates entries before this matters; the bound only
	// caps growth from many distinct rooms per session.
	authzCacheMax = 1 << 16
	// authzCompactHead compacts the FIFO ring once its consumed prefix is
	// at least this long, keeping the ring's memory proportional to live
	// entries plus a bounded stale prefix.
	authzCompactHead = 4096
)

// authzCache is the hub's publication authorization cache: a token -> user
// map with FIFO eviction, guarded by one mutex. The publish path touches it
// once per publish (all lookups under a single acquisition), never per
// recipient, so concurrent publishers serialize only on the short lookup
// pass.
type authzCache struct {
	mu   sync.Mutex
	m    map[authzKey]*authzEntry
	ring []authzKey // FIFO insertion order; eviction consumes ring[head]
	head int
}

func newAuthzCache() *authzCache {
	return &authzCache{m: make(map[authzKey]*authzEntry)}
}

// store records the query result for key under the given generations,
// updating an existing entry in place (its FIFO slot stays put). The store
// must only be used with the generations the query ran under; a later lookup
// with moved generations misses and re-queries.
func (c *authzCache) store(key authzKey, userID, sess, member, userVersion int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.m[key]; e != nil {
		e.userID = userID
		e.sess, e.member, e.userVersion = sess, member, userVersion
		return
	}
	if len(c.ring)-c.head >= authzCacheMax {
		old := c.ring[c.head]
		c.head++
		delete(c.m, old)
		if c.head >= authzCompactHead && c.head*2 >= len(c.ring) {
			copy(c.ring, c.ring[c.head:])
			c.ring = c.ring[:len(c.ring)-c.head]
			c.head = 0
		}
	}
	c.m[key] = &authzEntry{userID: userID, sess: sess, member: member, userVersion: userVersion}
	c.ring = append(c.ring, key)
}

// authorize resolves every recipient's authorized session user. Cache hits
// are read under one lock acquisition; misses run one AuthorizedSessions
// query per distinct room (the same snapshot query as the legacy path) and
// are stored back under the generations they were queried with. A recipient
// left at user 0 is unauthorized and cancelled by the caller. Query errors
// resolve every pending recipient of that room as unauthorized but store
// nothing, so a transient read failure cannot poison the cache.
func (h *Hub) authorize(ctx context.Context, recipients []recipient) {
	sess := h.db.SessionVersion()
	member := h.db.MembershipVersion()
	userVersion := h.db.UserVersion()
	type miss struct {
		index int
		key   authzKey
	}
	var misses []miss
	h.authz.mu.Lock()
	// ENGINE-61: the recipients of one publish overwhelmingly share a few
	// (room, token) keys (at 1000 clients a room publish is one token), and
	// hashing each token for the cache map is ~100ns a recipient. The stack
	// memo resolves repeated keys from the first hit, so the pass touches the
	// map once per distinct key instead of once per recipient. Entries are
	// only ever filled from cache hits read under the same lock section, so a
	// later store (it needs the lock) cannot alias a memo entry; misses still
	// fall through to the map as before.
	var memo [4]struct {
		key  authzKey
		user int64
	}
	memoLen := 0
	for i := range recipients {
		r := &recipients[i]
		key := authzKey{room: r.room, token: r.client.token}
		j := 0
		for ; j < memoLen; j++ {
			if memo[j].key == key {
				r.user = memo[j].user
				break
			}
		}
		if j < memoLen {
			continue
		}
		if e := h.authz.m[key]; e != nil && e.sess == sess && e.member == member && e.userVersion == userVersion {
			r.user = e.userID
			if memoLen < len(memo) {
				memo[memoLen] = struct {
					key  authzKey
					user int64
				}{key, e.userID}
				memoLen++
			}
			continue
		}
		misses = append(misses, miss{i, key})
	}
	h.authz.mu.Unlock()
	if len(misses) == 0 {
		return
	}
	// Batch one query per distinct room for the missing tokens.
	type roomQuery struct {
		tokens  []string
		allowed map[string]int64
	}
	rooms := make(map[int64]*roomQuery, 1)
	for _, m := range misses {
		q := rooms[m.key.room]
		if q == nil {
			q = &roomQuery{}
			rooms[m.key.room] = q
		}
		q.tokens = append(q.tokens, m.key.token)
	}
	for room, q := range rooms {
		allowed, err := h.db.AuthorizedSessions(ctx, q.tokens, room)
		if err != nil {
			allowed = nil
		}
		q.allowed = allowed
	}
	// Resolve every miss from the fresh query results and store them back
	// under the generations the queries ran with. The cache lock is taken
	// per store, not across the pass: a store on a revoked result can never
	// be served, because its generations are the pre-revocation ones and a
	// lookup requires all three to match. Query errors store nothing, so a
	// transient read failure cannot poison the cache with negatives.
	for _, m := range misses {
		allowed := rooms[m.key.room].allowed
		recipients[m.index].user = allowed[m.key.token]
		if allowed != nil {
			h.authz.store(m.key, allowed[m.key.token], sess, member, userVersion)
		}
	}
}

// publishScratch is the per-publish working set: the wire-frame bytes and the
// recipient snapshot. Pooled so a steady-state broadcast allocates nothing
// beyond the frame itself (ENGINE-57): wrap holds the once-escaped frame
// JSON, recips the recipients. A pooled scratch is private to one publish —
// the frame cache clones whatever bytes it retains (NewPreparedMessage
// copies, cache entries own their frames) — so reuse is safe.
type publishScratch struct {
	wrap   []byte
	recips []recipient
}

var publishScratchPool = sync.Pool{
	New: func() any {
		return &publishScratch{wrap: make([]byte, 0, 1024), recips: make([]recipient, 0, 64)}
	},
}

// memoEntry is one per-publish identifier→frame memo slot; frameMemoLen is
// the stack-array bound before a publish falls back to a map for its memo.
// A room or stream broadcast fans out over one identifier in practice (the
// identifier encodes the room or stream), and a handful of the extras in
// mixed publishes; the array covers those with zero allocation.
type memoEntry struct {
	identifier string
	frame      *websocket.PreparedMessage
}

const frameMemoLen = 8

func (h *Hub) publish(ctx context.Context, room int64, name string, message any) {
	roomScope := ""
	if room != 0 {
		roomScope = fmt.Sprintf("room:%d", room)
	}
	scratch := publishScratchPool.Get().(*publishScratch)
	wrap := scratch.wrap[:0]
	recipients := scratch.recips[:0]
	defer func() {
		scratch.wrap, scratch.recips = wrap, recipients
		publishScratchPool.Put(scratch)
	}()
	// Snapshot the recipients from the publish index under the hub lock only;
	// authorization and delivery run without it (ENGINE-40b). The pooled
	// slice is grown to the recipient count when smaller, so the snapshot is
	// one amortized allocation with no growth.
	h.mu.RLock()
	if room != 0 {
		m := h.roomStreams[room]
		if cap(recipients) < len(m) {
			recipients = make([]recipient, 0, len(m))
		}
		for c, ids := range m {
			for _, identifier := range ids {
				recipients = append(recipients, recipient{c, identifier, room, roomScope, 0})
			}
		}
	}
	if name != "" {
		m := h.namedStreams[name]
		if len(recipients) == 0 && cap(recipients) < len(m) {
			recipients = make([]recipient, 0, len(m))
		}
		for c, ids := range m {
			for _, identifier := range ids {
				recipients = append(recipients, recipient{c, identifier, 0, name, 0})
			}
		}
	}
	h.mu.RUnlock()
	if len(recipients) == 0 {
		return
	}
	if h.fast {
		h.authorize(ctx, recipients)
	} else {
		// Legacy: recheck every publication, batching distinct sessions per
		// room (the pre-ENGINE-40b behavior, unchanged).
		groups := make(map[int64]map[string]struct{})
		for _, r := range recipients {
			if groups[r.room] == nil {
				groups[r.room] = make(map[string]struct{})
			}
			groups[r.room][r.client.token] = struct{}{}
		}
		allowed := make(map[int64]map[string]int64, len(groups))
		for groupRoom, tokens := range groups {
			keys := make([]string, 0, len(tokens))
			for token := range tokens {
				keys = append(keys, token)
			}
			var err error
			allowed[groupRoom], err = h.db.AuthorizedSessions(ctx, keys, groupRoom)
			if err != nil {
				allowed[groupRoom] = nil
			}
		}
		for i := range recipients {
			recipients[i].user = allowed[recipients[i].room][recipients[i].client.token]
		}
	}

	// ENGINE-57: encode once, share bytes. The payload is escaped exactly
	// once per publish — a string payload straight into the pooled scratch
	// (appendFrameUTF8), any other payload by one json.Marshal spliced into
	// each wrapper (appendFrameJSON) — and every subscriber of an identifier
	// receives the same immutable PreparedMessage, never a re-encode. The
	// frame cache still keys on (scope, identifier, exact payload) with its
	// FIFO bounds; a hit skips the wrapper entirely. Per-recipient work is a
	// memo scan (stack array, map fallback beyond frameMemoLen distinct
	// identifiers) and the channel send.
	var messageJSON []byte
	payloadString, stringPayload := "", false
	if s, ok := message.(string); ok {
		payloadString, stringPayload = s, true
	} else {
		var err error
		if messageJSON, err = json.Marshal(message); err != nil {
			return
		}
	}
	var memo [frameMemoLen]memoEntry
	memoLen := 0
	var extra map[string]*websocket.PreparedMessage
	for i := range recipients {
		r := &recipients[i]
		if r.user != r.client.user.ID {
			r.client.stop()
			continue
		}
		ident := r.identifier
		frame, found := (*websocket.PreparedMessage)(nil), false
		for j := 0; j < memoLen; j++ {
			if memo[j].identifier == ident {
				frame, found = memo[j].frame, true
				break
			}
		}
		if !found && extra != nil {
			if f, ok := extra[ident]; ok {
				frame, found = f, true
			}
		}
		if !found {
			wrap = wrap[:0]
			if stringPayload {
				wrap = appendFrameUTF8(wrap, ident, payloadString)
			} else {
				wrap = appendFrameJSON(wrap, ident, messageJSON)
			}
			if h.fast {
				frame = h.cache.lookupOrCreate(r.scope, ident, wrap)
			} else {
				frame = websocket.NewPreparedMessage(websocket.MessageText, wrap)
			}
			if memoLen < frameMemoLen {
				memo[memoLen] = memoEntry{ident, frame}
				memoLen++
			} else {
				if extra == nil {
					extra = make(map[string]*websocket.PreparedMessage, 4)
				}
				extra[ident] = frame
			}
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
