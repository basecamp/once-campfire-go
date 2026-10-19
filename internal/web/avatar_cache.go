package web

// avatarCache caches signed avatar URLs. The value is a pure function of the
// user id, the "?v=" version stamp and the server's signing keys — the
// signed id embeds no timestamp and the model-purpose pipeline is fixed — so
// a cache hit is byte-identical to a fresh sign as long as the entry was
// produced by the same Server (one secrets; a cache instance never crosses
// secret generations).
//
// avatarURL runs per rendered message avatar — every post, message page and
// sidebar row — and the sign costs a JSON canonicalization, a base64 and an
// HMAC-SHA-256, so the cache turns the hottest per-message allocation into a
// map lookup. The map is bounded per shard; an evicted entry only costs the
// next render a fresh sign, never a wrong value.

import (
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/rails"
)

const (
	avatarCacheShards = 16
	avatarCacheCap    = 32 // entries per shard; 512 total
)

type avatarKey struct {
	id int64
	// v distinguishes the "?v=" stamp in the URL: 0 when updated is zero
	// (no stamp), otherwise uint64(updated.UTC().Unix())+1. Two updates in
	// the same second format to the same stamp and therefore the same URL,
	// so the key is exact by construction; the +1 reserves 0 for "no
	// stamp" so a user whose updated_at is exactly the unix epoch cannot
	// collide with it.
	v uint64
}

type avatarCache struct {
	shards [avatarCacheShards]struct {
		mu sync.Mutex
		m  map[avatarKey]string
	}
}

func newAvatarCache() *avatarCache {
	c := &avatarCache{}
	for i := range c.shards {
		c.shards[i].m = make(map[avatarKey]string, 16)
	}
	return c
}

func (c *avatarCache) shard(k avatarKey) *struct {
	mu sync.Mutex
	m  map[avatarKey]string
} {
	h := uint64(14695981039346656037)
	h ^= uint64(k.id)
	h *= 1099511628211
	h ^= k.v
	h *= 1099511628211
	return &c.shards[h&(avatarCacheShards-1)]
}

// avatarURL returns the signed avatar URL for (id, updated), filling the
// cache on a miss. The returned string is the cache's own and must not be
// mutated.
func avatarURL(c *avatarCache, secrets *rails.Secrets, id int64, updated time.Time) string {
	k := avatarKey{id: id}
	if !updated.IsZero() {
		k.v = uint64(updated.UTC().Unix()) + 1
	}
	sh := c.shard(k)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if url, ok := sh.m[k]; ok {
		return url
	}
	url := signedAvatar(secrets, id, updated)
	if len(sh.m) >= avatarCacheCap {
		for victim := range sh.m {
			delete(sh.m, victim)
			break
		}
	}
	sh.m[k] = url
	return url
}
