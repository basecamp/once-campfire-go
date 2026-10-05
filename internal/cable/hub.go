// Package cable implements Action Cable for Campfire as the reference's cable crate does
// (reference/crates/cable, and the channels in reference/crates/campfire/src/channels): the
// actioncable-v1-json protocol, Campfire's channels, and the in-process pub/sub that stands in
// for Redis.
//
// Broadcasts are routed by stream name. A stream's subscribers that share a channel identifier
// form a group, and each broadcast is wrapped once per group into one frame all its members
// write. Channels authorize when they're subscribed; afterwards, the writes that take access away
// (ban, deactivation, sign-out, losing a room membership) disconnect the user's connections.
package cable

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
)

type Hub struct {
	db      *database.DB
	secrets *rails.Secrets

	mu          sync.RWMutex
	streams     map[string][]*group     // broadcasting → its subscribers, by identifier
	connections map[int64][]*connection // user → open connections, for remote disconnects
	closed      bool

	beatEvery time.Duration
	heartbeat sync.Once
	beat      atomic.Pointer[beat]
	stop      chan struct{}
	live      sync.WaitGroup
}

// group is the subscribers of one stream that subscribed with the same identifier, and so
// receive identical frames (the reference's pubsub Group).
type group struct {
	identifier string
	encoded    []byte // the identifier as a JSON string
	members    []*subscription
}

// beat is one heartbeat: the ping frame every connection sends, and a channel closed when the
// next beat replaces it (the reference's watch channel).
type beat struct {
	frame *websocket.PreparedMessage
	next  chan struct{}
}

func New(db *database.DB, secrets *rails.Secrets) *Hub {
	return &Hub{db: db, secrets: secrets, streams: map[string][]*group{}, connections: map[int64][]*connection{}, beatEvery: beatInterval, stop: make(chan struct{})}
}

// Publish appends markup (a Turbo Stream) to a room's messages for its RoomMessagesChannel
// subscribers.
func (h *Hub) Publish(_ context.Context, room int64, markup string) {
	h.broadcast(roomMessages(room), appendString(nil, markup))
}

// PublishStream is ActionCable.server.broadcast(name, message).
func (h *Hub) PublishStream(_ context.Context, name string, message any) {
	payload, err := encode(message)
	if err != nil {
		return
	}
	h.broadcast(name, payload)
}

// Disconnect closes every connection of the user and tells the clients not to reconnect
// (User#deactivate, User::Bannable#ban).
func (h *Hub) Disconnect(user int64) { h.disconnect(user, remoteFinal) }

// Reconnect closes every connection of the user and tells the clients to reconnect, which
// resubscribes them to what they may still see (User#reset_remote_connections: sign-out and a
// destroyed membership).
func (h *Hub) Reconnect(user int64) { h.disconnect(user, remoteReconnect) }

func (h *Hub) disconnect(user int64, frame *websocket.PreparedMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.connections[user] {
		c.close(frame)
	}
}

// Close disconnects every connection with server_restart, as the reference does on shutdown,
// and waits a while for them to finish closing.
func (h *Hub) Close() {
	h.mu.Lock()
	if !h.closed {
		h.closed = true
		close(h.stop)
	}
	for _, conns := range h.connections {
		for _, c := range conns {
			c.close(serverRestart)
		}
	}
	h.mu.Unlock()
	finished := make(chan struct{})
	go func() {
		h.live.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(closeTimeout):
	}
}

// broadcast queues an encoded payload for every subscriber of stream, wrapped once per group
// into one frame its members share. It returns how many subscribers it reached.
func (h *Hub) broadcast(stream string, payload []byte) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	reached := 0
	for _, g := range h.streams[stream] {
		shared := frame(message(g.encoded, payload))
		for _, s := range g.members {
			s.conn.enqueue(s, shared)
		}
		reached += len(g.members)
	}
	return reached
}

// join starts delivering s's stream to its connection.
func (h *Hub) join(s *subscription) {
	if s.stream == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	groups := h.streams[s.stream]
	i := slices.IndexFunc(groups, func(g *group) bool { return g.identifier == s.identifier })
	if i < 0 {
		i = len(groups)
		h.streams[s.stream] = append(groups, &group{identifier: s.identifier, encoded: appendString(nil, s.identifier)})
	}
	g := h.streams[s.stream][i]
	s.group, s.index = g, len(g.members)
	g.members = append(g.members, s)
}

func (h *Hub) leave(s *subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remove(s)
}

// remove takes s out of its group, and the group out of its stream once it's empty. h.mu must
// be held for writing.
func (h *Hub) remove(s *subscription) {
	g := s.group
	if g == nil {
		return
	}
	s.group = nil
	last := len(g.members) - 1
	g.members[s.index] = g.members[last]
	g.members[s.index].index = s.index
	g.members[last] = nil
	g.members = g.members[:last]
	if last > 0 {
		return
	}
	groups := h.streams[s.stream]
	i := slices.Index(groups, g)
	groups = slices.Delete(groups, i, i+1)
	if len(groups) == 0 {
		delete(h.streams, s.stream)
	} else {
		h.streams[s.stream] = groups
	}
}

// register lists c among its user's connections, unless the hub has closed.
func (h *Hub) register(c *connection) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.live.Add(1)
	h.connections[c.user.ID] = append(h.connections[c.user.ID], c)
	return true
}

// unregister is Connection::Base#handle_close: every subscription is removed, and a
// PresenceChannel's membership marked absent.
func (h *Hub) unregister(c *connection) {
	defer h.live.Done()
	h.mu.Lock()
	conns := slices.DeleteFunc(h.connections[c.user.ID], func(other *connection) bool { return other == c })
	if len(conns) == 0 {
		delete(h.connections, c.user.ID)
	} else {
		h.connections[c.user.ID] = conns
	}
	for _, s := range c.subscriptions {
		h.remove(s)
	}
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	for _, s := range c.subscriptions {
		if s.present {
			h.db.Presence(ctx, c.user.ID, s.room, "absent")
		}
	}
}

// startHeartbeat starts the server-wide heartbeat on the first connection, as Rails'
// setup_heartbeat_timer does, so every connection pings in step with one shared frame.
func (h *Hub) startHeartbeat() {
	h.heartbeat.Do(func() {
		h.beat.Store(newBeat())
		go func() {
			ticker := time.NewTicker(h.beatEvery)
			defer ticker.Stop()
			for {
				select {
				case <-h.stop:
					return
				case <-ticker.C:
					close(h.beat.Swap(newBeat()).next)
				}
			}
		}()
	})
}

func newBeat() *beat {
	return &beat{frame: frame(ping(time.Now().Unix())), next: make(chan struct{})}
}

// roomMessages names a room's message stream. Turbo::StreamsChannel refuses every name ending
// in ":messages", so only RoomMessagesChannel, which checks membership, streams from it.
func roomMessages(room int64) string { return strconv.FormatInt(room, 10) + ":messages" }

// readRooms is ReadRoomsChannel's stream: the user's rooms read in another window.
func readRooms(user int64) string { return fmt.Sprintf("user_%d_reads", user) }

// unreadRooms is UnreadRoomsChannel's stream: activity in the user's rooms.
func unreadRooms(user int64) string { return fmt.Sprintf("user_%d_unreads", user) }
