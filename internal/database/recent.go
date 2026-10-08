package database

import (
	"context"
	"sync"
	"sync/atomic"
)

// Latest message windows and content generations live in memory. A room page
// reads the same forty ids on every hit; search and sidebar HTML stay valid
// until a write bumps the generation. Readers fall back to SQLite on a miss.
// Commits from another connection move the watcher's data_version and drop
// these caches even when this process did not write.
type readState struct {
	mu       sync.Mutex
	watchMu  sync.Mutex
	gen      atomic.Uint64
	epoch    uint64
	seen     uint32
	have     bool
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
// that search and sidebar HTML depend on is committed, including commits
// from another connection to the same file.
func (d *DB) ContentGeneration() uint64 {
	d.syncExternal(context.Background())
	if s := d.state(); s != nil {
		return s.gen.Load()
	}
	return 0
}

// UserGeneration is the per-user revision (presence, involvement) plus the
// shared content generation.
func (d *DB) UserGeneration(user int64) (userGen, content uint64) {
	d.syncExternal(context.Background())
	s := d.state()
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	userGen = s.userGen[user]
	s.mu.Unlock()
	return userGen, s.gen.Load()
}

func (d *DB) dataVersion(ctx context.Context) (uint32, error) {
	s := d.state()
	if d == nil || d.watch == nil || s == nil {
		return 0, nil
	}
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	var version int64
	err := d.watch.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version)
	return uint32(version), err
}

// syncExternal drops in-memory reads when another connection has committed
// since the last sample. The first sample only records the baseline.
func (d *DB) syncExternal(ctx context.Context) {
	version, err := d.dataVersion(ctx)
	s := d.state()
	if s == nil {
		return
	}
	if err != nil {
		s.mu.Lock()
		s.dropLocked()
		s.have = false
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.have {
		s.seen = version
		s.have = true
		return
	}
	if version == s.seen {
		return
	}
	s.dropLocked()
	s.seen = version
}

// lockedVersion re-samples after BEGIN. syncExternal drops caches when the
// watcher moved; the returned value is the version covered by that drop.
func (d *DB) lockedVersion(ctx context.Context) (uint32, error) {
	d.syncExternal(ctx)
	return d.dataVersion(ctx)
}

// finishExternal runs after our commit. The watcher also moves for that
// commit, and a foreign commit after the lock drops can collapse into the
// same observation. A version other than the in-lock origin is therefore
// not "only our write": drop the content generation as well as the window
// and the account row. Sidebar and search HTML are keyed by that generation.
func (d *DB) finishExternal(ctx context.Context, origin uint32) {
	version, err := d.dataVersion(ctx)
	s := d.state()
	if s == nil {
		return
	}
	if err != nil {
		s.mu.Lock()
		s.dropLocked()
		s.have = false
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if version == origin {
		s.seen = version
		s.have = true
		return
	}
	s.dropLocked()
	s.seen = version
	s.have = true
}

func (s *readState) dropLocked() {
	s.gen.Add(1)
	s.epoch++
	s.rooms = map[int64][]Message{}
	s.hasAcct = false
	s.account = Account{}
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

// InvalidateAccount drops the cached account row after logo or other
// attachment changes that bypass UpdateAccount.
func (d *DB) InvalidateAccount() {
	d.clearAccount()
	d.changed()
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

func (d *DB) beginRoom(room int64) (uint64, uint64) {
	s := d.state()
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epoch, s.roomGen[room]
}

func (d *DB) storeLatest(room int64, epoch, seen uint64, msgs []Message) {
	s := d.state()
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.epoch != epoch || s.roomGen[room] != seen {
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
