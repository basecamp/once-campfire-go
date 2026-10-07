package web

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/fastdb"
)

// This file is the ENGINE-43/44 leftover read caches: the per-request reads
// that survive the refs/sidebar/search caches — the room row plus its
// membership check, the account row render reads on every page, the
// room-page invitation probe, and home/search's original-room fallback.
//
// Every entry is keyed by the version counters its data depends on, so a hit
// is validated by key equality alone and nothing is invalidated by hand:
//
//   - Room rows (rooms JOIN memberships) depend on the rooms and memberships
//     tables AND on rooms.updated_at, which message create/edit/delete/boost
//     move as a content version (the refs cache and the page shell key on
//     it). The sidebar registry covers table changes and the corpus counter
//     covers message writes — the same two counters the search cache keys on
//     — so the key is (user, room, SidebarVersion, CorpusVersion). Every
//     write that changes a room row, a membership or a message bumps one of
//     them (audited in internal/database/versions_test.go), so the key never
//     outlives the data it validated.
//   - The account row (a single global row read on every page) depends on the
//     accounts table and the account-logo attachment (the HasLogo EXISTS), so
//     the key is (SidebarVersion, logoVersion): UpdateAccount bumps the
//     registry and sets updated_at; deleteLogo bumps the web-side logoVersion
//     counter, the one account-visible write that touches neither table row.
//   - The invitation probe (room == first room AND message count <= 40)
//     depends on the room set and the room's message count, so the key is
//     (room, CorpusVersion, MembershipVersion): room creation and destruction
//     move membership/corpus, and every message create/edit/delete moves
//     corpus (the counter the search cache already relies on).
//   - OriginalRoom (the last member's room for a user) depends on rooms and
//     memberships, so the key is (user, SidebarVersion).
//
// The counters are deliberately coarse (any write in the covered tables
// misses every entry of that kind), which is the completeness guarantee: a
// cache hit can only be served for a version that every relevant write has
// moved past. Poisoning tests pin the overwrite contract (an entry carries
// its key and a lookup verifies it, so a digest/key collision reads as a
// miss), and the invalidation tests run real write helpers and assert the
// next lookup misses.
//
// The value copied out of an entry on a hit is a snapshot: callers never
// mutate the cached room/account structs, so it is safe to share them.

// readCacheKind tags which read a key/value pair belongs to.
type readCacheKind uint8

const (
	kindRoom readCacheKind = iota
	kindAccount
	kindInvitation
	kindOriginalRoom
)

// readCacheKey is the composite key for one cached read. Fields are named by
// role per kind (see key builders below); version carries the registry
// counter and v2 the second counter where a read depends on two.
type readCacheKey struct {
	kind    readCacheKind
	a, b    int64
	version uint64
	v2      int64
}

// readCacheEntry is one cached value plus the key it was stored under (so a
// lookup re-verifies it) and the charged byte size.
type readCacheEntry struct {
	key        readCacheKey
	room       database.Room
	account    database.Account
	invitation bool
	originalID int64
	found      bool
	bytes      int
}

// readCache is a byte- and entry-bounded LRU over the four read kinds, safe
// for concurrent use. The zero value is not usable; build with newReadCache.
// The policies mirror the refs cache: reject entries larger than limit/4,
// charge a fixed overhead, prune oldest-first to 75% of the budget, and cap
// the entry count.
type readCache struct {
	mu         sync.Mutex
	entries    map[readCacheKey]*list.Element
	order      list.List
	bytes      int
	limit      int
	maxEntries int
}

// readCacheMaxEntries caps the entry count so a server with many rooms cannot
// grow the map without bound even when every entry is tiny.
const readCacheMaxEntries = 8192

// readCacheOverhead is the fixed per-entry bookkeeping charge.
const readCacheOverhead = 240

func newReadCache(limit int) *readCache {
	return &readCache{entries: make(map[readCacheKey]*list.Element), limit: limit, maxEntries: readCacheMaxEntries}
}

// Enabled reports whether the cache can store anything.
func (c *readCache) Enabled() bool { return c != nil && c.limit/4 > 0 }

func (c *readCache) lookup(key readCacheKey) (readCacheEntry, bool) {
	if c == nil {
		return readCacheEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return readCacheEntry{}, false
	}
	entry := e.Value.(readCacheEntry)
	if entry.key != key {
		return readCacheEntry{}, false // key verification: a collision is a miss
	}
	c.order.MoveToFront(e)
	return entry, true
}

func (c *readCache) store(key readCacheKey, makeEntry func() (readCacheEntry, int)) {
	if !c.Enabled() {
		return
	}
	entry, size := makeEntry()
	entry.key, entry.bytes = key, size
	if size > c.limit/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.entries[key]; ok {
		c.bytes -= previous.Value.(readCacheEntry).bytes
		c.order.Remove(previous)
	}
	c.entries[key] = c.order.PushFront(entry)
	c.bytes += size
	for c.bytes > c.limit*3/4 || len(c.entries) > c.maxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		evicted := oldest.Value.(readCacheEntry)
		c.bytes -= evicted.bytes
		delete(c.entries, evicted.key)
		c.order.Remove(oldest)
	}
}

// roomRowCached returns the room for user,id with its membership check,
// through the read cache when it is enabled: the key carries the sidebar and
// corpus versions read before the lookup, so a hit is valid for the rooms/
// memberships/messages state this request would have read; a miss reads
// through fastdb (c) or database/sql exactly as before. A missing or
// forbidden room is ErrNoRows, cached as not-found.
func (s *Server) roomRowCached(c *fastdb.Conn, ctx context.Context, user, id int64) (database.Room, error) {
	if s.readCache == nil {
		return s.roomRow(c, ctx, user, id)
	}
	version, err := s.DB.SidebarVersion(ctx)
	if err != nil {
		return s.roomRow(c, ctx, user, id)
	}
	key := readCacheKey{kind: kindRoom, a: user, b: id, version: version, v2: s.DB.CorpusVersion()}
	if entry, ok := s.readCache.lookup(key); ok {
		if !entry.found {
			return database.Room{}, sql.ErrNoRows
		}
		return entry.room, nil
	}
	room, err := s.roomRow(c, ctx, user, id)
	found := err == nil
	if !found && !errors.Is(err, sql.ErrNoRows) {
		return room, err
	}
	s.readCache.store(key, func() (readCacheEntry, int) {
		entry := readCacheEntry{found: found}
		copy := room
		entry.room = copy
		size := readCacheOverhead + 32 + len(copy.Name) + len(copy.Type)
		return entry, size
	})
	return room, err
}

// accountCached returns the account row render reads on every page, through
// the read cache when it is enabled. The key carries the sidebar version
// (which UpdateAccount bumps after every account write) and the logo version
// (which deleteLogo bumps after detaching the logo attachment), so a hit is
// valid for the account state this request would have read. Only ErrNoRows is
// cached as not-found; any other error falls through un-cached.
func (s *Server) accountCached(ctx context.Context) (database.Account, error) {
	if s.readCache == nil {
		return s.DB.Account(ctx)
	}
	version, err := s.DB.SidebarVersion(ctx)
	if err != nil {
		return s.DB.Account(ctx)
	}
	key := readCacheKey{kind: kindAccount, a: s.logoVersion.Load(), version: version}
	if entry, ok := s.readCache.lookup(key); ok {
		if !entry.found {
			return database.Account{}, sql.ErrNoRows
		}
		return entry.account, nil
	}
	a, err := s.DB.Account(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return a, err
	}
	found := err == nil
	s.readCache.store(key, func() (readCacheEntry, int) {
		entry := readCacheEntry{found: found}
		copy := a
		entry.account = copy
		size := readCacheOverhead + 96 + len(copy.Name) + len(copy.JoinCode) + len(copy.CustomStyles) + len(copy.Settings)
		return entry, size
	})
	return a, err
}

// invitationCached returns the room-page invitation probe, through the read
// cache when it is enabled. The key carries the corpus and membership
// versions that every room-set and message-count change moves, so a hit is
// valid for the probe's inputs this request would have read.
func (s *Server) invitationCached(c *fastdb.Conn, ctx context.Context, room int64) (bool, error) {
	if s.readCache == nil {
		return s.invitation(c, ctx, room)
	}
	key := readCacheKey{kind: kindInvitation, a: room, version: uint64(s.DB.CorpusVersion()), v2: s.DB.MembershipVersion()}
	if entry, ok := s.readCache.lookup(key); ok {
		return entry.invitation, nil
	}
	value, err := s.invitation(c, ctx, room)
	if err != nil {
		return value, err
	}
	s.readCache.store(key, func() (readCacheEntry, int) {
		return readCacheEntry{invitation: value, found: true}, readCacheOverhead + 24
	})
	return value, nil
}

// originalRoomCached returns database.DB.OriginalRoom's result, through the
// read cache when it is enabled. The key carries the sidebar version that
// every room/membership write moves.
func (s *Server) originalRoomCached(ctx context.Context, user int64) (int64, error) {
	if s.readCache == nil {
		return s.DB.OriginalRoom(ctx, user)
	}
	version, err := s.DB.SidebarVersion(ctx)
	if err != nil {
		return s.DB.OriginalRoom(ctx, user)
	}
	key := readCacheKey{kind: kindOriginalRoom, a: user, version: version}
	if entry, ok := s.readCache.lookup(key); ok {
		if !entry.found {
			return 0, sql.ErrNoRows
		}
		return entry.originalID, nil
	}
	id, err := s.DB.OriginalRoom(ctx, user)
	found := err == nil
	if !found && !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	s.readCache.store(key, func() (readCacheEntry, int) {
		return readCacheEntry{originalID: id, found: found}, readCacheOverhead + 16
	})
	return id, err
}
