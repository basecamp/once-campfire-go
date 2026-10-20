package presentation

import (
	"container/list"
	"encoding/binary"
	"html/template"
	"strconv"
	"strings"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

type fragmentEntry struct {
	key   string
	html  template.HTML
	bytes int
	part  responsebody.Part
	shell *templateShell
}
type fragmentCache struct {
	mu           sync.Mutex
	entries      map[string]*list.Element
	order        list.List
	bytes, limit int
}

func newFragmentCache(limit int) *fragmentCache {
	return &fragmentCache{entries: map[string]*list.Element{}, limit: limit}
}
func (c *fragmentCache) get(key string) (template.HTML, bool) {
	entry, ok := c.entry(key)
	return entry.html, ok
}
func (c *fragmentCache) entry(key string) (fragmentEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return fragmentEntry{}, false
	}
	c.order.MoveToFront(e)
	return e.Value.(fragmentEntry), true
}

// Like the reference's MemoryStore, retain the first rendered value for a version,
// charge 240 bytes per entry, reject oversized entries, and prune to 75% capacity.
func (c *fragmentCache) put(key string, html template.HTML) template.HTML {
	return c.putEntry(fragmentEntry{key: key, html: html}).html
}
func (c *fragmentCache) putEntry(entry fragmentEntry) fragmentEntry {
	key := entry.key
	size := len(key) + len(entry.html) + entry.part.Len() + 240
	if entry.shell != nil {
		// Charge owned payload once, plus the slice and Part headers/digests.
		size += entry.shell.bytes + 32 + 56*len(entry.shell.parts)
	}
	if size > c.limit/4 {
		return entry
	}
	entry.bytes = size
	// Callers prepare owned payloads before admission. Oversized/disabled-cache
	// responses remain usable, and hits never wait for copying or hashing.
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(fragmentEntry)
	}
	c.entries[key] = c.order.PushFront(entry)
	c.bytes += size
	if c.bytes > c.limit {
		for c.bytes > c.limit*3/4 {
			e := c.order.Back()
			entry := e.Value.(fragmentEntry)
			c.bytes -= entry.bytes
			delete(c.entries, entry.key)
			c.order.Remove(e)
		}
	}
	return entry
}

const messageVersionSize = 20

// Match Stamp's UTC, microsecond-truncated identity without formatting dates.
// Separate seconds and fractions avoid UnixNano/UnixMicro's narrower date range.
func messageVersion(message database.MessageReference) [messageVersionSize]byte {
	var version [messageVersionSize]byte
	binary.LittleEndian.PutUint64(version[:8], uint64(message.ID))
	binary.LittleEndian.PutUint64(version[8:16], uint64(message.UpdatedAt.Unix()))
	binary.LittleEndian.PutUint32(version[16:], uint32(message.UpdatedAt.Nanosecond()/1000))
	return version
}

func messageCacheKey(message database.MessageReference) string {
	version := messageVersion(message)
	return "message/" + string(version[:])
}

func messageListCacheKey(messages []database.MessageReference) string {
	var key strings.Builder
	key.Grow(len("message-list/") + messageVersionSize*len(messages))
	key.WriteString("message-list/")
	for _, message := range messages {
		version := messageVersion(message)
		key.Write(version[:])
	}
	return key.String()
}

// MessageScope is supplied by a scoped query observation, never by a receipt.
// A zero generation disables persisted-fragment reuse and admission.
type MessageScope struct {
	Generation uint64
	Facts      Facts
}

func (s MessageScope) key(key string) string {
	if s.Generation == 0 {
		return ""
	}
	return strconv.FormatUint(s.Generation, 10) + "/" + strconv.Quote(s.Facts.Host) + "/" + strconv.Quote(s.Facts.Origin) + "/" + key
}

// Fragments owns the single budget shared by message fragments, lists and shells.
// It consumes owned display data and has no persistence or request dependency.
type Fragments struct {
	renderer *Renderer
	cache    *fragmentCache
}

func NewFragments(renderer *Renderer, limit int) *Fragments {
	return &Fragments{renderer: renderer, cache: newFragmentCache(limit)}
}
func (f *Fragments) MessageList(scope MessageScope, refs []database.MessageReference) (responsebody.Part, bool) {
	if scope.Generation == 0 {
		return responsebody.Part{}, false
	}
	entry, ok := f.cache.entry(scope.key(messageListCacheKey(refs)))
	return entry.part, ok
}
func (f *Fragments) Message(scope MessageScope, ref database.MessageReference) (template.HTML, bool) {
	if scope.Generation == 0 {
		return "", false
	}
	return f.cache.get(scope.key(messageCacheKey(ref)))
}
func (f *Fragments) Messages(scope MessageScope, prepared PreparedMessages, data map[int64]database.MessageDisplay, users map[int64]database.UserDisplay) ([]MessageView, error) {
	views, err := f.renderer.Messages(scope.Facts, prepared, data, users)
	if err != nil {
		return nil, err
	}
	if scope.Generation != 0 {
		for i := range views {
			if data[views[i].ID].Author == nil {
				continue
			}
			ref := prepared.Records[i].Reference()
			if html, ok := f.Message(scope, ref); ok {
				views[i].Fragment = html
			} else {
				views[i].Fragment = f.cache.put(scope.key(messageCacheKey(ref)), views[i].Fragment)
			}
		}
	}
	return views, nil
}
func (f *Fragments) RecordMessages(scope MessageScope, refs []database.MessageReference, fragments []template.HTML) responsebody.Part {
	entry := fragmentEntry{key: scope.key(messageListCacheKey(refs)), part: FragmentList(fragments)}
	if scope.Generation != 0 {
		entry = f.cache.putEntry(entry)
	}
	return entry.part
}
