package web

import (
	"container/list"
	"crypto/sha256"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// This file is the message-page reference cache (ENGINE-19). The room and
// messages pages both re-run the same 40-row MessagePageReferences scan on
// every request even when nothing changed; the reference list is only a
// function of the room's content, and rooms.updated_at (in the room row both
// routes already read for membership) moves on every message
// create/edit/delete/boost. So the scan result is cached keyed by
// (roomID, pageVersion, anchor, direction) and a hit is validated by the
// room-version component of the key alone: a request that read the same
// rooms.updated_at value reads the same reference window. A write changes the
// version and the key misses; nothing is invalidated by hand.
//
// The cached value deliberately holds only references (id, room, update
// stamp), never rich text or author rows: full message rows hydrate in
// messageItems exactly where they hydrate today — on a per-message fragment
// cache miss. That is the same shape the legacy "before" direction already
// used, so one hydration path serves every direction.
//
// The messages-page validator (ETag, Last-Modified, Cache-Control) is a pure
// function of the references too, so it is precomputed once per fill instead
// of rebuilt per request (the 40× Sprintf/Format/ReplaceAll + sha256 the
// profile attributed to messageFreshness); the per-request work is one header
// set and the conditional check.

// messageRefsDefaultMB is the byte budget when CAMPFIRE_MESSAGE_REFS_CACHE_MB
// is unset. References are small (40 refs ≈ 2.3 KB charged), so the default is
// generous per room; the entry cap is the other bound.
const messageRefsDefaultMB = 8

// messageRefsMaxEntries caps the entry count so a server serving many rooms
// cannot grow the map without bound even when every entry is tiny.
const messageRefsMaxEntries = 4096

// messageRefsKey identifies one reference window. version is the room's
// updated-at stamp in UnixMicro at fill time (the precision the database
// stores); a hit requires the request's room row to carry the same value.
type messageRefsKey struct {
	room      int64
	version   int64
	anchor    int64
	direction string
}

// messageRefsEntry is one cached window: the reference list plus the
// precomputed messages-page validator (both Turbo-Frame variants, byte-for-byte
// what the removed per-request messageFreshness computed). The refs slice is
// immutable by convention — every reader copies structs out of it, the same
// ownership rule piececache.Entry documents — so a hit returns it without a
// copy. key rides on the entry so an eviction is O(1), the same arrangement
// fragmentEntry uses.
type messageRefsEntry struct {
	key       messageRefsKey
	refs      []database.Message
	etag      string
	etagFrame string
	modified  time.Time
	bytes     int
}

// messageRefsCache is a byte- and entry-bounded LRU of reference windows,
// safe for concurrent use. The zero value is not usable; call
// newMessageRefsCache. The policies mirror the fragment cache: reject entries
// larger than limit/4 (one window must not thrash the budget), charge a fixed
// bookkeeping overhead, and prune oldest-first to 75% of the budget.
type messageRefsCache struct {
	mu         sync.Mutex
	entries    map[messageRefsKey]*list.Element
	order      list.List
	bytes      int
	limit      int
	maxEntries int
}

func newMessageRefsCache(limit int) *messageRefsCache {
	return &messageRefsCache{entries: make(map[messageRefsKey]*list.Element), limit: limit, maxEntries: messageRefsMaxEntries}
}

// Enabled reports whether the cache can store anything. A zero limit means
// every store is rejected and every lookup misses, keeping the callers free of
// a nil check (CAMPFIRE_MESSAGE_REFS_CACHE_MB=0).
func (c *messageRefsCache) Enabled() bool { return c != nil && c.limit/4 > 0 }

// lookup returns the window stored under key, moving it to the front of the
// LRU. A nil cache misses.
func (c *messageRefsCache) lookup(key messageRefsKey) (messageRefsEntry, bool) {
	if c == nil {
		return messageRefsEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return messageRefsEntry{}, false
	}
	c.order.MoveToFront(e)
	return e.Value.(messageRefsEntry), true
}

// store publishes a window under key, replacing any previous window for the
// same key in one lock acquisition. refs is copied into the entry so the
// caller's request slice (which may carry full records) is not retained;
// only the references the page assembly needs are kept. A window too large
// for the budget is not stored; a key already present keeps its recency.
func (c *messageRefsCache) store(key messageRefsKey, refs []database.Message, validator messageValidator) {
	if c == nil {
		return
	}
	size := 240 + 40*len(refs) + len(validator.etag) + len(validator.etagFrame)
	if size > c.limit/4 {
		return
	}
	entry := messageRefsEntry{
		key:       key,
		refs:      make([]database.Message, len(refs)),
		etag:      validator.etag,
		etagFrame: validator.etagFrame,
		modified:  validator.modified,
		bytes:     size,
	}
	for i := range refs {
		entry.refs[i] = database.Message{ID: refs[i].ID, RoomID: refs[i].RoomID, UpdatedAt: refs[i].UpdatedAt}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.entries[key]; ok {
		c.bytes -= previous.Value.(messageRefsEntry).bytes
		c.order.Remove(previous)
	}
	c.entries[key] = c.order.PushFront(entry)
	c.bytes += size
	// Same policy as the fragment and piece caches: prune to 75% of the
	// budget, and additionally cap the entry count.
	for c.bytes > c.limit*3/4 || len(c.entries) > c.maxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		evicted := oldest.Value.(messageRefsEntry)
		c.bytes -= evicted.bytes
		delete(c.entries, evicted.key)
		c.order.Remove(oldest)
	}
}

// messageValidator carries the messages page's conditional-GET validator,
// precomputed from the cached references. The zero value is not usable.
type messageValidator struct {
	etag      string
	etagFrame string
	modified  time.Time
}

// apply writes the validator headers and reports whether the request is
// conditionally fresh (a 304 was written) — the exact contract the removed
// per-request messageFreshness served: the same ETag (frame variant selected
// by the Turbo-Frame header), Last-Modified, Cache-Control, and the same
// notModified precedence of If-None-Match over If-Modified-Since.
func (v messageValidator) apply(w http.ResponseWriter, r *http.Request) bool {
	etag := v.etag
	if r.Header.Get("Turbo-Frame") != "" {
		etag = v.etagFrame
	}
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Last-Modified", v.modified.UTC().Format(http.TimeFormat))
	h.Set("Cache-Control", "max-age=0, private, must-revalidate")
	return notModified(w, r, etag, v.modified)
}

// messageValidatorOf precomputes the messages-page validator from message
// references. The digest input is built exactly as conditional.go's former
// messageFreshness built it — per message "messages/<id>-<stamp without
// dots>", joined with "/", suffixed "/messages/index" plus "/frame" when a
// Turbo-Frame header is present — so the cached validator is byte-for-byte
// what the per-request rebuild produced, minus the rebuilding.
func messageValidatorOf(refs []database.Message) messageValidator {
	var parts strings.Builder
	var modified time.Time
	for _, m := range refs {
		if m.UpdatedAt.After(modified) {
			modified = m.UpdatedAt
		}
		parts.WriteString("messages/")
		parts.WriteString(strconv.FormatInt(m.ID, 10))
		parts.WriteByte('-')
		parts.WriteString(strings.ReplaceAll(m.UpdatedAt.UTC().Format("20060102150405.000000"), ".", ""))
		parts.WriteByte('/')
	}
	prefix := strings.TrimSuffix(parts.String(), "/")
	var v messageValidator
	v.etag = weakETag(sha256.Sum256([]byte(prefix + "/messages/index")))
	v.etagFrame = weakETag(sha256.Sum256([]byte(prefix + "/frame/messages/index")))
	v.modified = modified
	return v
}
