package web

import (
	"strings"
	"sync"
	"time"
)

// The auth fast path (ENGINE-42, behind CAMPFIRE_AUTH_FAST, default on)
// follows the Elixir PR #5 shape: cache the verified session_token cookie
// with its signed expiry, re-check the expiry against the current time on
// every request, and keep the session/user re-check on the per-request read
// path. Tampered, malformed and expired values are never cached: only a full
// rails.Secrets.VerifyCookieExpires success inserts an entry, and a lookup
// refuses (and drops) any entry whose signed expiry has passed or whose
// signing-key fingerprint does not match the current secret generation.

// parseAuthFast maps a CAMPFIRE_AUTH_FAST value to its setting, accepting the
// same shapes as parseFastDB. An unrecognised value reports valid=false so the
// caller can warn while keeping the default on rather than silently changing
// behaviour.
func parseAuthFast(raw string) (enabled, valid bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "on", "true", "1":
		return true, true
	case "off", "false", "0":
		return false, true
	default:
		return true, false
	}
}

const (
	authCacheShards = 64
	authCacheCap    = 128 // entries per shard; 8192 total
)

// verifiedCookie is one cached verification: the recovered token, the
// envelope's signed expiry, and the signing-key fingerprint the value was
// verified under.
type verifiedCookie struct {
	token   string
	expires time.Time
	fp      [8]byte
}

// verifiedCookieShard guards one map; the shards bound the lock contention on
// the hit path (typically one request in flight per slot at bench concurrency).
type verifiedCookieShard struct {
	mu sync.Mutex
	m  map[string]verifiedCookie
}

// verifiedCookieCache maps an unescaped session_token cookie value to its
// verified token. It never decides validity: eviction and expiry/secret
// refusals only turn a hit into a miss, and a miss runs the full verification,
// so the cache cannot serve a value the full path would reject.
type verifiedCookieCache struct {
	shards [authCacheShards]verifiedCookieShard
}

func newVerifiedCookieCache() *verifiedCookieCache {
	c := &verifiedCookieCache{}
	for i := range c.shards {
		c.shards[i].m = make(map[string]verifiedCookie, 16)
	}
	return c
}

// shard picks the slot for a cookie value. FNV-1a over the whole value; the
// value is attacker-chosen, so the hash only balances the shards, it is not a
// security boundary.
func (c *verifiedCookieCache) shard(raw string) *verifiedCookieShard {
	h := uint64(14695981039346656037)
	for i := 0; i < len(raw); i++ {
		h ^= uint64(raw[i])
		h *= 1099511628211
	}
	return &c.shards[h&(authCacheShards-1)]
}

// lookup returns the verified token for raw when the cache holds a value
// whose signing-key fingerprint matches fp and whose signed expiry is still in
// the future at now. An expired or foreign-secret entry is dropped so the next
// request pays a full verification (which rejects it).
func (c *verifiedCookieCache) lookup(raw string, now time.Time, fp [8]byte) (string, bool) {
	sh := c.shard(raw)
	sh.mu.Lock()
	e, ok := sh.m[raw]
	if !ok {
		sh.mu.Unlock()
		return "", false
	}
	if e.fp != fp || !now.Before(e.expires) {
		delete(sh.m, raw)
		sh.mu.Unlock()
		return "", false
	}
	sh.mu.Unlock()
	return e.token, true
}

// store records a full verification. The cache is bounded per shard; at the
// cap one arbitrary entry is evicted (Go map iteration starts at a random
// bucket), which only costs the next request a verification.
func (c *verifiedCookieCache) store(raw, token string, expires time.Time, fp [8]byte) {
	sh := c.shard(raw)
	sh.mu.Lock()
	if _, ok := sh.m[raw]; !ok && len(sh.m) >= authCacheCap {
		for victim := range sh.m {
			delete(sh.m, victim)
			break
		}
	}
	sh.m[raw] = verifiedCookie{token: token, expires: expires, fp: fp}
	sh.mu.Unlock()
}

// remove drops a value (used on logout, so the revoked token is not served
// from cache; the per-request session read would reject it either way).
func (c *verifiedCookieCache) remove(raw string) {
	sh := c.shard(raw)
	sh.mu.Lock()
	delete(sh.m, raw)
	sh.mu.Unlock()
}

// verifiedSessionToken returns the session token carried by an unescaped
// session_token cookie value, or the VerifyCookie error when it is invalid.
// With the auth fast path enabled a bounded cache serves previously verified
// values whose signed expiry still holds and whose secret generation matches;
// the caller still re-checks the session and user through the read path on
// every request. With the fast path off this is exactly
// Secrets.VerifyCookie, byte for byte.
func (s *Server) verifiedSessionToken(raw string, now time.Time) (string, error) {
	if s.authCache != nil {
		if token, ok := s.authCache.lookup(raw, now, s.Secrets.SigningFingerprint()); ok {
			return token, nil
		}
		var token string
		expires, err := s.Secrets.VerifyCookieExpires("session_token", raw, now, &token)
		if err == nil && !expires.IsZero() {
			// Only an expiry-carrying verified value is cached; a cookie
			// without a signed expiry (which our signer never produces) stays
			// on the full-verify path for every request.
			s.authCache.store(raw, token, expires, s.Secrets.SigningFingerprint())
		}
		return token, err
	}
	var token string
	err := s.Secrets.VerifyCookie("session_token", raw, now, &token)
	return token, err
}
