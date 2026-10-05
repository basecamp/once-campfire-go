// Package piececache stores immutable, content-versioned response pieces —
// raw bytes plus a complete gzip member of those bytes — in a byte-bounded LRU
// and assembles ordered piece lists into a response with no allocation on the
// warm path: up to eight pieces into a caller buffer with enough capacity
// allocates nothing. Two documented exceptions: more than eight pieces uses
// per-piece record scratch, and a caller buffer too small for the encoded
// pieces grows once.
//
// Keys are content versions (a room's message version, a user's session
// version), not URLs: an entry is never updated, so a version bump simply
// misses and the next request stores a fresh piece. Entries returned by
// NewEntry and Put are immutable; Get hands out the same pointer with no copy,
// and a replacement under one key publishes a new entry instead of editing the
// old one. Readers may keep and use an entry after it has been evicted or
// replaced. Immutability holds by convention: every reader shares one entry,
// so a caller must never write through Entry.Raw or Entry.Member.
//
// No HTTP or template types cross this boundary: callers bring version strings
// and complete gzip members, and take away bytes and a digest.
package piececache

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"
	"sync"
)

// DefaultLimit is the byte budget when CAMPFIRE_FRAGMENT_CACHE_MB is unset,
// matching the legacy fragment cache's 32 MiB.
const DefaultLimit = 32 << 20

// entryOverhead is the fixed per-entry bookkeeping charge, the same 240 bytes
// the fragment cache uses (internal/web/fragments.go).
const entryOverhead = 240

// Entry is one immutable response piece: the raw bytes, a complete gzip member
// of those bytes (header, deflate stream and CRC32/ISIZE trailer as produced
// by compress/gzip), and the SHA-256 digest of the raw bytes that the response
// ETag is computed from.
//
// Once published, an Entry is never mutated. NewEntry and Put take copies, so
// the caller's buffers may be reused immediately afterwards. Immutability holds
// by convention: callers must never write through Raw or Member, because every
// reader of that key shares the same entry.
type Entry struct {
	// Raw is the uncompressed payload.
	Raw []byte
	// Member is a complete gzip member of Raw.
	Member []byte
	// Digest is SHA-256 of Raw.
	Digest [32]byte
}

// NewEntry returns an immutable entry for raw and member, copying both. Member
// is trusted to be a gzip member of raw; it is not re-verified (the caller
// just compressed it, and decompression on the request path would defeat the
// point of the cache). NewEntry is exported for per-request dynamic pieces
// that are assembled once but not cached; Put uses it for stored pieces.
func NewEntry(raw, member []byte) *Entry {
	entry := &Entry{
		Raw:    append([]byte(nil), raw...),
		Member: append([]byte(nil), member...),
	}
	entry.Digest = sha256.Sum256(entry.Raw)
	return entry
}

// Cache is a byte-bounded LRU of immutable pieces, safe for concurrent use.
// The zero value is not usable; call New.
type Cache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   list.List
	bytes   int
	limit   int
}

// item is the list element payload. key and bytes live here, not on Entry, so
// an entry handed to a reader carries no cache bookkeeping.
type item struct {
	key   string
	bytes int
	entry *Entry
}

// New returns a cache bounded to limit bytes. A non-positive limit disables
// storage: every Put is rejected and every Get misses, keeping the API usable
// without a nil check (CAMPFIRE_FRAGMENT_CACHE_MB=0 does the same to the
// legacy fragment cache).
func New(limit int) *Cache {
	return &Cache{entries: make(map[string]*list.Element), limit: limit}
}

// Get returns the entry stored under key, or nil on a miss. A hit moves the
// entry to the front of the LRU and returns the immutable view without
// copying.
func (c *Cache) Get(key string) *Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return nil
	}
	c.order.MoveToFront(element)
	return element.Value.(*item).entry
}

// Put copies raw and member into a new immutable entry, computes the SHA-256
// digest of raw, and — when it fits — publishes it under key, replacing any
// previous entry in one lock acquisition. It reports false when the charged
// size (len(key) + len(raw) + len(member) + entryOverhead) exceeds limit/4, so
// one piece can never thrash the whole budget; the returned entry is still
// valid and independent of the caller's buffers, it is simply not cached. A
// rejected Put changes nothing in the cache, so an oversized replacement
// leaves the previous entry in place.
//
// The copies and the digest are computed outside the mutex; only the map/list
// swap is critical. A concurrent reader therefore observes either the old
// entry or the new one, both complete — never a half-updated mix.
func (c *Cache) Put(key string, raw, member []byte) (*Entry, bool) {
	size := len(key) + len(raw) + len(member) + entryOverhead
	entry := NewEntry(raw, member)
	if size > c.limit/4 {
		// Not cacheable, but the caller still needs the immutable piece for
		// this response; return it uncached rather than forcing a re-copy.
		return entry, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.entries[key]; ok {
		c.bytes -= previous.Value.(*item).bytes
		c.order.Remove(previous)
	}
	c.bytes += size
	c.entries[key] = c.order.PushFront(&item{key: key, bytes: size, entry: entry})
	if c.bytes > c.limit {
		// Same policy as internal/web/fragments.go: prune oldest-first to 75%
		// of the budget, not merely back under it.
		for c.bytes > c.limit*3/4 {
			oldest := c.order.Back()
			if oldest == nil {
				break
			}
			evicted := oldest.Value.(*item)
			c.bytes -= evicted.bytes
			delete(c.entries, evicted.key)
			c.order.Remove(oldest)
		}
	}
	return entry, true
}

// LimitFromEnv returns the cache byte limit: DefaultLimit when
// CAMPFIRE_FRAGMENT_CACHE_MB is unset, otherwise that many mebibytes. The key
// is the one the legacy fragment cache reads (internal/web/server.go), so one
// deployment knob sizes both caches independently; values outside [0, 1<<20]
// or non-numeric are an error, exactly as the legacy parser treats them. A
// caller must treat the error as startup-fatal, mirroring web.New, rather than
// serving with an unintended cache budget.
func LimitFromEnv() (int, error) {
	megabytes := DefaultLimit >> 20
	if raw, ok := os.LookupEnv("CAMPFIRE_FRAGMENT_CACHE_MB"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 1<<20 {
			return 0, fmt.Errorf("invalid CAMPFIRE_FRAGMENT_CACHE_MB %q", raw)
		}
		megabytes = parsed
	}
	return megabytes << 20, nil
}
