package front

import (
	"bytes"
	"compress/gzip"
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

const (
	gzipCacheBytes = 32 << 20
	gzipMaxBody    = 1 << 20
	gzipMaxEntry   = 256 << 10
	gzipEntryCost  = 128 // key, map slot, list node and slice header
	gzipMaxFlights = 16
)

type gzipKey struct {
	digest [32]byte
	mtime  uint32
}

type gzipEntry struct {
	key  gzipKey
	body []byte
}

type gzipFlight struct {
	done chan struct{}
	body []byte
	err  error
}

// This is a compression memo, not an HTTP response cache. Every request still
// runs the application (including authorization) and supplies its own headers.
// Strong part digests key the memo: weak/resource ETags cannot identify exact bytes.
// No plain bodies, cookies, request state or response headers are retained.
type gzipCache struct {
	mu       sync.Mutex
	entries  map[gzipKey]*list.Element
	order    list.List
	flights  map[gzipKey]*gzipFlight
	capacity int
	size     int
}

func newGzipCache(capacity int) *gzipCache {
	return &gzipCache{capacity: capacity, entries: make(map[gzipKey]*list.Element), flights: make(map[gzipKey]*gzipFlight)}
}

func (c *gzipCache) prepare(ctx context.Context, parts []responsebody.Part, mtime uint32) ([]byte, error) {
	key := gzipKey{digest: responsebody.Digest(parts), mtime: mtime}
	c.mu.Lock()
	if entry := c.entries[key]; entry != nil {
		c.order.MoveToFront(entry)
		body := entry.Value.(gzipEntry).body
		c.mu.Unlock()
		return body, nil
	}
	if flight := c.flights[key]; flight != nil {
		c.mu.Unlock()
		select {
		case <-flight.done:
			return flight.body, flight.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Bound in-flight bookkeeping; saturation falls back to independent
	// compression rather than queuing unrelated responses behind one lock.
	var flight *gzipFlight
	if len(c.flights) < gzipMaxFlights {
		flight = &gzipFlight{done: make(chan struct{})}
		c.flights[key] = flight
	}
	c.mu.Unlock()

	body, err := gzipBody(parts, mtime)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && len(body)+gzipEntryCost <= min(gzipMaxEntry, c.capacity/8) {
		if existing := c.entries[key]; existing != nil {
			body = existing.Value.(gzipEntry).body
			c.order.MoveToFront(existing)
		} else {
			// Do not keep a compressor's oversized backing buffer.
			body = bytes.Clone(body)
			cost := cap(body) + gzipEntryCost
			if cost <= min(gzipMaxEntry, c.capacity/8) {
				for c.size+cost > c.capacity {
					old := c.order.Back()
					entry := old.Value.(gzipEntry)
					delete(c.entries, entry.key)
					c.size -= cap(entry.body) + gzipEntryCost
					c.order.Remove(old)
				}
				c.entries[key] = c.order.PushFront(gzipEntry{key, body})
				c.size += cost
			}
		}
	}
	if flight != nil {
		flight.body, flight.err = body, err
		delete(c.flights, key)
		close(flight.done)
	}
	return body, err
}

func gzipBody(parts []responsebody.Part, mtime uint32) ([]byte, error) {
	var output bytes.Buffer
	writer := gzipPool.Get().(*gzip.Writer)
	writer.Reset(&output)
	writer.Header.OS = 3
	if mtime != 0 {
		writer.Header.ModTime = time.Unix(int64(mtime), 0)
	}
	defer func() {
		writer.Reset(nil)
		gzipPool.Put(writer)
	}()
	for _, part := range parts {
		if _, err := part.WriteTo(writer); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
