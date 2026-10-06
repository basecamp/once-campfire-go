package views

import (
	"container/list"
	"fmt"
	"hash/fnv"
	"strconv"
	"sync"
	"time"
)

// FragmentCache is the process's fragment store (`cache record do ... end`), the reference's
// crates/views/src/fragment_cache.rs: bounded by bytes the way ActiveSupport::Cache::MemoryStore
// is. Each entry counts its key, its payload and PerEntryOverhead bytes; when a write takes the
// total past the limit, least recently used entries go until it's back to three quarters of it.
// Reads count as uses. An entry larger than a quarter of the limit is returned but not kept.
type FragmentCache struct {
	maxBytes int
	mu       sync.Mutex
	entries  map[string]*list.Element
	recency  *list.List // front: most recently used
	bytes    int
}

type cacheEntry struct {
	key   string
	value any
	size  int
}

// DefaultMaxBytes is MemoryStore's default size, 32 MB.
const DefaultMaxBytes = 32 << 20

// PerEntryOverhead is what an entry costs beyond its key and payload (MemoryStore's).
const PerEntryOverhead = 240

func NewFragmentCache(maxBytes int) *FragmentCache {
	return &FragmentCache{maxBytes: maxBytes, entries: map[string]*list.Element{}, recency: list.New()}
}

// Fetch is `Rails.cache.fetch(key) { render }` for a rendered fragment. A nil cache renders
// uncached (perform_caching = false).
func (c *FragmentCache) Fetch(key string, render func() string) *Fragment {
	if c == nil {
		return NewFragment(render())
	}
	if f, ok := c.Get(key).(*Fragment); ok {
		return f
	}
	f := NewFragment(render())
	return c.write(key, f, len(key)+len(f.HTML)+PerEntryOverhead).(*Fragment)
}

// FetchValue is `Rails.cache.fetch(key) { value }` for any value of the given size.
func (c *FragmentCache) FetchValue(key string, size int, compute func() any) any {
	if c == nil {
		return compute()
	}
	if value := c.Get(key); value != nil {
		return value
	}
	return c.write(key, compute(), len(key)+size+PerEntryOverhead)
}

// Get is the value key holds, if any (a use, for eviction).
func (c *FragmentCache) Get(key string) any {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return nil
	}
	c.recency.MoveToFront(element)
	return element.Value.(*cacheEntry).value
}

// Fragment is the fragment key holds, if any, for callers that gather a fragment's inputs only on
// a miss.
func (c *FragmentCache) Fragment(key string) *Fragment {
	f, _ := c.Get(key).(*Fragment)
	return f
}

// fragmentBytes is Fragment for a key built in a buffer, looked up without copying it into a
// string.
func (c *FragmentCache) fragmentBytes(key []byte) *Fragment {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[string(key)]
	if !ok {
		return nil
	}
	c.recency.MoveToFront(element)
	f, _ := element.Value.(*cacheEntry).value.(*Fragment)
	return f
}

// write stores value unless key already holds one, and returns what key holds: when two renders
// of a key race, the first one stored is what both return.
func (c *FragmentCache) write(key string, value any, size int) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.recency.MoveToFront(element)
		return element.Value.(*cacheEntry).value
	}
	if size > c.maxBytes/4 {
		return value
	}
	c.entries[key] = c.recency.PushFront(&cacheEntry{key: key, value: value, size: size})
	c.bytes += size
	if c.bytes > c.maxBytes {
		// MemoryStore#prune(@max_size * 0.75)
		target := c.maxBytes / 4 * 3
		for c.bytes > target {
			oldest := c.recency.Back()
			if oldest == nil {
				break
			}
			entry := c.recency.Remove(oldest).(*cacheEntry)
			delete(c.entries, entry.key)
			c.bytes -= entry.size
		}
	}
	return value
}

func (c *FragmentCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Bytes is what the entries account for.
func (c *FragmentCache) Bytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// Digest is the template digest part of a key: a stable hash of the template sources a fragment
// renders. Only its stability within the process matters.
func Digest(sources ...string) string {
	h := fnv.New64a()
	for _, source := range sources {
		h.Write([]byte(source))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// AppendCacheVersion appends `Time#to_fs(:usec)` of a record's updated_at, its cache_version:
// %Y%m%d%H%M%S and six digits of microseconds, in UTC, digit by digit.
func AppendCacheVersion(b []byte, t time.Time) []byte {
	t = t.UTC()
	b = appendPadded(b, t.Year(), 4)
	b = appendPadded(b, int(t.Month()), 2)
	b = appendPadded(b, t.Day(), 2)
	b = appendPadded(b, t.Hour(), 2)
	b = appendPadded(b, t.Minute(), 2)
	b = appendPadded(b, t.Second(), 2)
	return appendPadded(b, t.Nanosecond()/1000, 6)
}

func appendPadded(b []byte, value, width int) []byte {
	var digits [20]byte
	for i := width - 1; i >= 0; i-- {
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return append(b, digits[:width]...)
}

// AppendCacheKeyWithVersion appends `record.cache_key_with_version`: "messages/1-20240601120000000000".
func AppendCacheKeyWithVersion(b []byte, table string, id int64, updatedAt time.Time) []byte {
	b = append(b, table...)
	b = append(b, '/')
	b = strconv.AppendInt(b, id, 10)
	b = append(b, '-')
	return AppendCacheVersion(b, updatedAt)
}

// AppendRecordFragmentKey appends a record fragment's key,
// `views/<template>:<digest>/<record cache_key_with_version>` (CacheHelper#fragment_name_with_digest).
func AppendRecordFragmentKey(b []byte, template, digest, table string, id int64, updatedAt time.Time) []byte {
	b = append(b, "views/"...)
	b = append(b, template...)
	b = append(b, ':')
	b = append(b, digest...)
	b = append(b, '/')
	return AppendCacheKeyWithVersion(b, table, id, updatedAt)
}
