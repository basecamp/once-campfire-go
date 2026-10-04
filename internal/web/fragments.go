package web

import (
	"container/list"
	"context"
	"crypto/sha256"
	"github.com/basecamp/once-campfire-go/internal/database"
	"html/template"
	"strconv"
	"strings"
	"sync"
)

type fragmentEntry struct {
	key      string
	html     template.HTML
	bytes    int
	digest   [32]byte
	payload  []byte
	segments []shellSegment
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
	key, html := entry.key, entry.html
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(fragmentEntry)
	}
	size := len(key) + len(html) + 240
	for _, segment := range entry.segments {
		size += len(segment.part.data)
	}
	var payload []byte
	if strings.HasPrefix(key, "message-list/") {
		payload = []byte(html)
		size += len(payload)
	}
	if size > c.limit/4 {
		return entry
	}
	entry.bytes, entry.digest, entry.payload = size, sha256.Sum256([]byte(html)), payload
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
func messageCacheKey(message database.Message) string {
	return string(appendMessageCacheKey(nil, message))
}
func appendMessageCacheKey(key []byte, message database.Message) []byte {
	key = append(key, "message/"...)
	key = database.AppendStamp(key, message.UpdatedAt)
	key = append(key, '/')
	return strconv.AppendInt(key, message.ID, 10)
}
func (s *Server) messageItems(ctx context.Context, messages []database.Message) ([]messageView, error) {
	views := viewMessages(messages)
	var missing []int64
	for i, m := range messages {
		if html, ok := s.fragments.get(messageCacheKey(m)); ok {
			views[i].Fragment = html
		} else if m.CreatorID == 0 {
			missing = append(missing, m.ID)
		}
	}
	hydrated := make(map[int64]database.Message, len(missing))
	if len(missing) > 0 {
		loaded, err := s.DB.MessagesByID(ctx, missing)
		if err != nil {
			return nil, err
		}
		for _, message := range loaded {
			hydrated[message.ID] = message
		}
	}
	// Render the uncached messages together so their rooms, creators, boosts and
	// attachments are loaded once for all of them.
	var render []database.Message
	var positions []int
	for i, m := range messages {
		if views[i].Fragment != "" {
			continue
		}
		if m.CreatorID == 0 {
			var found bool
			m, found = hydrated[m.ID]
			if !found {
				views[i].Fragment = unrenderableMessage
				continue
			}
		}
		render = append(render, m)
		positions = append(positions, i)
	}
	if len(render) > 0 {
		rendered, err := s.messageViews(ctx, render)
		if err != nil {
			return nil, err
		}
		for j, i := range positions {
			views[i] = rendered[j]
		}
	}
	return views, nil
}

const unrenderableMessage template.HTML = `<div class="message message--formatted message--failed center"><div class="message__body"><div class="message__body-content txt-align-center">Failed to load message content</div></div></div>`
