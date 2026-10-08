package web

import (
	"container/list"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"html/template"
	"strconv"
	"strings"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/database"
)

type fragmentEntry struct {
	key                         string
	html                        template.HTML
	bytes                       int
	digest                      [32]byte
	payload                     []byte
	messageMarker, loadedMarker string
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
	size := len(key) + len(html) + len(entry.messageMarker) + len(entry.loadedMarker) + 240
	// fragmentKey namespaces message-list keys with the observed generation
	// and origin, so the list marker is a path segment, not a prefix.
	listEntry := strings.HasPrefix(key, "message-list/") || strings.Contains(key, "/message-list/")
	var payload []byte
	if listEntry {
		payload = []byte(html)
		size += len(payload)
	}
	if size > c.limit/4 {
		return entry
	}
	// The digest feeds recorded-page ETags, which only ever hash the
	// message-list payload; a single-message fragment's digest is never
	// read, so only message-list entries pay for the hash (ENGINE-45b).
	entry.bytes, entry.payload = size, payload
	if listEntry {
		entry.digest = sha256.Sum256([]byte(html))
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
func messageCacheKey(message database.Message) string {
	return "message/" + database.Stamp(message.UpdatedAt) + "/" + strconv.FormatInt(message.ID, 10)
}

// Namespace timestamp fragments by the generation observed before request reads.
// An older in-flight render cannot populate a newer generation after a commit.
func (s *Server) fragmentKey(ctx context.Context, key string) string {
	version := uint64(0)
	var host, origin string
	if info := requestMetadata(ctx); info != nil {
		version = info.databaseVersion
		host, origin = info.host, info.origin
	} else {
		version, _ = s.DB.ResponseVersion(ctx)
	}
	if version == 0 {
		return "uncached/" + rand.Text() + "/" + key
	}
	return strconv.FormatUint(version, 10) + "/" + strconv.Quote(host) + "/" + strconv.Quote(origin) + "/" + key
}

func (s *Server) messageItems(ctx context.Context, messages []database.Message) ([]messageView, error) {
	views := viewMessages(messages)
	var missing []int64
	for i, m := range messages {
		if html, ok := s.fragments.get(s.fragmentKey(ctx, messageCacheKey(m))); ok {
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

		rendered, err := s.messageViews(ctx, []database.Message{m})
		if err != nil {
			return nil, err
		}
		views[i] = rendered[0]
	}
	return views, nil
}

const unrenderableMessage template.HTML = `<div class="message message--formatted message--failed center"><div class="message__body"><div class="message__body-content txt-align-center">Failed to load message content</div></div></div>`
