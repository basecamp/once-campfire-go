package database

import (
	"sync"
	"sync/atomic"
)

// Latest message windows and content generations live in memory. A room page
// reads the same forty ids on every hit; search and sidebar HTML stay valid
// until a write bumps the generation. Readers fall back to SQLite on a miss.
type readState struct {
	mu       sync.Mutex
	gen      atomic.Uint64
	roomGen  map[int64]uint64
	rooms    map[int64][]Message
	userGen  map[int64]uint64
	account  Account
	hasAcct  bool
	pageHits atomic.Uint64
	pageMiss atomic.Uint64
}

func newReadState() *readState {
	return &readState{roomGen: map[int64]uint64{}, rooms: map[int64][]Message{}, userGen: map[int64]uint64{}}
}

func (d *DB) state() *readState {
	if d == nil || d.reads == nil {
		return nil
	}
	return d.reads
}

// ContentGeneration changes when message, room, membership or account data
// that search and sidebar HTML depend on is committed.
func (d *DB) ContentGeneration() uint64 {
	if s := d.state(); s != nil {
		return s.gen.Load()
	}
	return 0
}

// UserGeneration is the per-user revision (presence, involvement) plus the
// shared content generation.
func (d *DB) UserGeneration(user int64) (userGen, content uint64) {
	s := d.state()
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	userGen = s.userGen[user]
	s.mu.Unlock()
	return userGen, s.gen.Load()
}

func (d *DB) changed() {
	if s := d.state(); s != nil {
		s.gen.Add(1)
	}
}

func (d *DB) Changed() { d.changed() }

func (d *DB) noteUser(user int64) {
	s := d.state()
	if s == nil {
		return
	}
	s.mu.Lock()
	s.userGen[user]++
	s.mu.Unlock()
}

func (d *DB) cachedAccount() (Account, bool) {
	s := d.state()
	if s == nil {
		return Account{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasAcct {
		return Account{}, false
	}
	return s.account, true
}

func (d *DB) storeAccount(a Account) {
	s := d.state()
	if s == nil {
		return
	}
	s.mu.Lock()
	s.account = a
	s.hasAcct = true
	s.mu.Unlock()
}

func (d *DB) clearAccount() {
	s := d.state()
	if s == nil {
		return
	}
	s.mu.Lock()
	s.hasAcct = false
	s.mu.Unlock()
}

func (d *DB) cachedLatest(room int64) ([]Message, bool) {
	s := d.state()
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.rooms[room]
	if msgs == nil {
		return nil, false
	}
	out := make([]Message, len(msgs))
	copy(out, msgs)
	return out, true
}

func (d *DB) countPage(hit bool) {
	s := d.state()
	if s == nil {
		return
	}
	if hit {
		s.pageHits.Add(1)
		return
	}
	s.pageMiss.Add(1)
}

func (d *DB) beginRoom(room int64) uint64 {
	s := d.state()
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roomGen[room]
}

func (d *DB) storeLatest(room int64, seen uint64, msgs []Message) {
	s := d.state()
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.roomGen[room] != seen {
		return
	}
	if _, ok := s.rooms[room]; ok {
		return
	}
	// A nil slice is a miss. An empty window is a hit, so the copy stays non-nil.
	s.rooms[room] = append([]Message{}, msgs...)
}

func (d *DB) noteCreate(m Message) {
	s := d.state()
	if s == nil {
		return
	}
	s.gen.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roomGen[m.RoomID]++
	msgs := s.rooms[m.RoomID]
	if msgs == nil {
		return
	}
	for _, existing := range msgs {
		if existing.ID == m.ID {
			return
		}
	}
	msgs = append(msgs, Message{ID: m.ID, RoomID: m.RoomID, UpdatedAt: m.UpdatedAt})
	if len(msgs) > 40 {
		msgs = append([]Message(nil), msgs[len(msgs)-40:]...)
	}
	s.rooms[m.RoomID] = msgs
}

func (d *DB) noteTouch(m Message) {
	s := d.state()
	if s == nil {
		return
	}
	s.gen.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roomGen[m.RoomID]++
	for i := range s.rooms[m.RoomID] {
		if s.rooms[m.RoomID][i].ID == m.ID {
			s.rooms[m.RoomID][i].UpdatedAt = m.UpdatedAt
		}
	}
}

func (d *DB) noteDelete(room int64) {
	s := d.state()
	if s == nil {
		return
	}
	s.gen.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roomGen[room]++
	delete(s.rooms, room)
}

func (d *DB) PageCacheStats() (hits, misses uint64) {
	s := d.state()
	if s == nil {
		return 0, 0
	}
	return s.pageHits.Load(), s.pageMiss.Load()
}

func (d *DB) ResetPageStats() {
	s := d.state()
	if s == nil {
		return
	}
	s.pageHits.Store(0)
	s.pageMiss.Store(0)
}
