package web

import (
	"crypto/sha256"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// This file is the ENGINE-30 search result cache: a bounded, byte-counted LRU
// of search pages keyed by (userID, normalized query, corpusVersion,
// membershipVersion, observed generation). A hit serves the cached page inputs —
// message fragments through the recorded piece path, no FTS scan, no Rooms read, no
// RecentSearches read — and only the version-visible state changes can make
// it miss.
//
// Versions come from database.DB.CorpusVersion/MembershipVersion, counters
// bumped after every committed FTS-affecting or membership-affecting write.
// The corpus counter is a single global value: a message anywhere in the
// account invalidates every cached query. That is deliberately conservative —
// it trades capacity for the guarantee that no entry outlives the corpus it
// was read from. The pinned-reader generation also invalidates external writes
// and DDL that do not update the local counters.
//
// The entry stores the search results (full message records, page order), the
// recent-searches list and nothing else: the search sidebar renders recent
// searches, and the unused rooms read was removed from both paths. Recent
// searches change through POST /searches and DELETE /searches/clear; both purge the
// user's entries, so a cached recent list is current by construction.
//
// Budget: CAMPFIRE_SEARCH_CACHE_MB (default 16) bytes of decoded page state,
// pruned oldest-first to 75% of the budget like the fragment cache. An entry
// larger than limit/4 is rejected outright. CAMPFIRE_SEARCH_CACHE=off (or a
// zero budget) disables the cache; the gateway lives in New and the search
// handler keeps the database/sql reads.

// searchResult is everything the search page renders that a cache hit must
// reproduce without database reads.
type searchResult struct {
	// recent is the user's recent-searches list at fill time.
	recent []string
	// messages are the search hits in page order (chronological), each with
	// the exact stamps the message fragment pieces are keyed by.
	messages []database.Message
}

// searchResultCache is a byte-bounded LRU of searchResult entries, safe for
// concurrent use (handlers run on many goroutines). The zero value is not
// usable; build with newSearchResultCache.
type searchResultCache struct {
	mu      sync.Mutex
	entries map[[32]byte]*listElement
	byUser  map[int64]*listElement
	order   searchList
	bytes   int
	limit   int
}

// listElement is the LRU payload: the digest key, the user for purges, the
// charged byte size and the immutable result. Entries are never mutated after
// publication; a replacement publishes a fresh element.
type listElement struct {
	key                [32]byte
	user               int64
	bytes              int
	result             searchResult
	prev               *listElement
	next               *listElement
	userPrev, userNext *listElement
}

// searchList is a minimal intrusive doubly-linked list for the LRU order
// (container/list would allocate an Element per entry; this one lives inside
// listElement).
type searchList struct {
	head, tail *listElement
}

func (l *searchList) pushFront(e *listElement) {
	e.prev, e.next = nil, l.head
	if l.head != nil {
		l.head.prev = e
	}
	l.head = e
	if l.tail == nil {
		l.tail = e
	}
}

func (l *searchList) remove(e *listElement) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		l.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		l.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (l *searchList) moveToFront(e *listElement) {
	if l.head == e {
		return
	}
	l.remove(e)
	l.pushFront(e)
}

// searchCacheBudget is the byte budget when CAMPFIRE_SEARCH_CACHE_MB is
// unset. Search results carry decoded message state (bodies, creators), so an
// entry runs to tens of kilobytes when a common term hits the 100-row cap;
// 16 MiB keeps a few hundred distinct queries in play.
const searchCacheBudget = 16 << 20

// searchEntryOverhead extends the fragment cache's 240-byte convention with
// intrusive user links and an amortized user-index map slot.
const searchEntryOverhead = 304

// messageFixedBytes approximates the struct cost of one database.Message
// (three int64s, three string headers, two time.Times).
const messageFixedBytes = 3*8 + 3*16 + 2*24

func searchResultBytes(r searchResult) int {
	size := searchEntryOverhead + 16
	for _, q := range r.recent {
		size += len(q) + 16
	}
	for i := range r.messages {
		m := &r.messages[i]
		size += messageFixedBytes + len(m.ClientID) + len(m.Body) + len(m.Creator)
	}
	return size
}

// newSearchResultCache returns a cache bounded to limit bytes. A non-positive
// limit disables storage: every put is rejected and every get misses, keeping
// the API usable without a nil check (CAMPFIRE_SEARCH_CACHE_MB=0 does the
// same to the result cache).
func newSearchResultCache(limit int) *searchResultCache {
	return &searchResultCache{entries: make(map[[32]byte]*listElement), byUser: make(map[int64]*listElement), limit: limit}
}

// searchCacheKey digests the five key components in place; the digest is the
// map key, so a lookup allocates nothing. The length-prefixed query keeps the
// digest input injective.
func searchCacheKey(user int64, query string, corpus, membership int64, generation uint64) [32]byte {
	var buf [128]byte
	b := strconv.AppendInt(buf[:0], user, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, corpus, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, membership, 10)
	b = append(b, ',')
	b = strconv.AppendUint(b, generation, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(len(query)), 10)
	b = append(b, ':')
	if len(query) <= len(buf)-len(b) {
		b = append(b, query...)
		return sha256.Sum256(b)
	}
	// Long queries keep the same digest input without growing the scratch.
	h := sha256.New()
	h.Write(b)
	for len(query) > 0 {
		n := copy(buf[:], query)
		h.Write(buf[:n])
		query = query[n:]
	}
	var digest [32]byte
	h.Sum(digest[:0])
	return digest
}

// get returns the cached result for the digest key, or false. Original key
// fields are not retained: identity relies on SHA-256 collision resistance,
// avoiding duplicate query storage. This is not full-key verification.
func (c *searchResultCache) get(user int64, query string, corpus, membership int64, generation uint64) (searchResult, bool) {
	key := searchCacheKey(user, query, corpus, membership, generation)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || e.key != key {
		return searchResult{}, false
	}
	c.order.moveToFront(e)
	return e.result, true
}

// put stores result under the key when it fits the budget. It replaces any
// earlier entry for the key and charges the decoded size. An entry over
// limit/4 (or a disabled cache) changes nothing.
func (c *searchResultCache) put(user int64, query string, corpus, membership int64, generation uint64, result searchResult) {
	if c.limit/4 <= 0 {
		return
	}
	key := searchCacheKey(user, query, corpus, membership, generation)
	size := searchResultBytes(result)
	if size > c.limit/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.entries[key]; ok {
		c.bytes -= previous.bytes
		c.order.remove(previous)
		c.unlinkUser(previous)
	}
	e := &listElement{key: key, user: user, bytes: size, result: result}
	c.entries[key] = e
	c.order.pushFront(e)
	e.userNext = c.byUser[user]
	if e.userNext != nil {
		e.userNext.userPrev = e
	}
	c.byUser[user] = e
	c.bytes += size
	if c.bytes > c.limit {
		for c.bytes > c.limit*3/4 {
			oldest := c.order.tail
			if oldest == nil {
				break
			}
			c.bytes -= oldest.bytes
			delete(c.entries, oldest.key)
			c.unlinkUser(oldest)
			c.order.remove(oldest)
		}
	}
}

// unlinkUser removes an entry from the user's intrusive index while c.mu is held.
func (c *searchResultCache) unlinkUser(e *listElement) {
	if e.userPrev != nil {
		e.userPrev.userNext = e.userNext
	} else if e.userNext != nil {
		c.byUser[e.user] = e.userNext
	} else {
		delete(c.byUser, e.user)
	}
	if e.userNext != nil {
		e.userNext.userPrev = e.userPrev
	}
}

// purgeUser drops every cached page after the user's recent-search list changes.
func (c *searchResultCache) purgeUser(user int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.byUser[user]; e != nil; e = e.userNext {
		c.bytes -= e.bytes
		c.order.remove(e)
		delete(c.entries, e.key)
	}
	delete(c.byUser, user)
}

// parseSearchCache maps a CAMPFIRE_SEARCH_CACHE value to its setting, with
// the same shapes and default-on policy as parseFastDB.
func parseSearchCache(raw string) (enabled, valid bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "on", "true", "1":
		return true, true
	case "off", "false", "0":
		return false, true
	default:
		return true, false
	}
}

// openSearchCache builds the search result cache honouring
// CAMPFIRE_SEARCH_CACHE (default on; off returns nil, and the handler keeps
// the database/sql reads — the A/B and rollback switch) and
// CAMPFIRE_SEARCH_CACHE_MB (default 16; 0 disables storage but keeps the
// enabled gate, mirroring how CAMPFIRE_FRAGMENT_CACHE_MB=0 treats the legacy
// fragment cache). An invalid size is a startup error, like the fragment
// cache's parser.
func openSearchCache() (*searchResultCache, error) {
	enabled := true
	if raw, ok := os.LookupEnv("CAMPFIRE_SEARCH_CACHE"); ok {
		var valid bool
		enabled, valid = parseSearchCache(raw)
		if !valid {
			slog.Warn("invalid CAMPFIRE_SEARCH_CACHE; keeping search cache on", "value", raw)
		}
	}
	megabytes := searchCacheBudget >> 20
	if raw, ok := os.LookupEnv("CAMPFIRE_SEARCH_CACHE_MB"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 1<<20 {
			return nil, &searchCacheError{raw: raw}
		}
		megabytes = parsed
	}
	if !enabled {
		slog.Info("search result cache", "enabled", false)
		return nil, nil
	}
	slog.Info("search result cache", "enabled", true, "cache_mib", megabytes)
	return newSearchResultCache(megabytes << 20), nil
}

// searchCacheError is the startup-fatal size error, mirroring the fragment
// cache's message shape.
type searchCacheError struct{ raw string }

func (e *searchCacheError) Error() string {
	return "invalid CAMPFIRE_SEARCH_CACHE_MB " + strconv.Quote(e.raw)
}
