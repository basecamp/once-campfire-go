// Package piececache stores immutable, content-versioned response pieces —
// raw bytes plus a raw DEFLATE fragment of those bytes (a block-stream ended
// at a byte-aligned, non-final boundary by a Flush, not a gzip member) — in a
// byte-bounded LRU and assembles ordered piece lists into a response with no
// allocation on the warm path: up to eight pieces into a caller buffer with
// enough capacity allocates nothing. Two documented exceptions: more than
// eight pieces uses per-piece record scratch, and a caller buffer too small
// for the encoded pieces grows once.
//
// Assembly for gzip clients splices the fragments into ONE gzip member: the
// RFC 1952 header, the concatenated fragments, a final empty stored block,
// and the CRC32/ISIZE trailer of the whole raw concatenation. RFC 1952
// permits multi-member streams and most decoders (Go, Python, curl, the Rust
// loadgen) decode them, but Chromium decodes only the first member, so a
// multi-member body renders the page shell without its messages in browsers;
// the single-member splice is the wire form every browser decodes. Per-piece
// CRC-32 values are stored at fill time and combined with the GF(2) matrix
// method (zlib's crc32_combine) at assembly, so the trailer costs no raw
// re-scan on the request path.
//
// Keys are content versions. Two key spaces share one budget and one recency
// order: string keys for version strings (a room's message version, a user's
// session version) and fixed-size [32]byte digest keys for callers that build
// a content identity in place (the recorded-response path in internal/web, so
// key construction allocates nothing per request). An entry is never updated,
// so a version bump simply misses and the next request stores a fresh piece.
// Entries returned by NewEntry and Put are immutable; Get hands out the same
// pointer with no copy, and a replacement under one key publishes a new entry
// instead of editing the old one. Readers may keep and use an entry after it
// has been evicted or replaced. Immutability holds by convention: every reader
// shares one entry, so a caller must never write through Entry.Raw or
// Entry.Fragment.
//
// No HTTP or template types cross this boundary: callers bring version strings
// and raw deflate fragments, and take away bytes and a digest.
package piececache

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"hash/crc32"
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

// Entry is one immutable response piece: the raw bytes, a raw DEFLATE
// fragment of those bytes (produced by compress/flate with Flush, so it ends
// at a byte-aligned non-final block boundary and splices with other
// fragments), an optional complete zstd frame of the same bytes, the CRC-32
// (IEEE) of the raw bytes, and the SHA-256 digest of the raw bytes that the
// response ETag is computed from. The zstd frame is present when the piece
// was filled with zstd frames enabled; a piece without it can still be
// assembled as identity or gzip.
//
// CRC, ZerosOp and Digest are derived from Raw and stored so the request path
// never re-scans the content: gzip assembly combines the per-piece CRCs with
// the stored zero operators, and the ETag hashes the digest records.
//
// Once published, an Entry is never mutated. NewEntry and Put take copies, so
// the caller's buffers may be reused immediately afterwards. Immutability holds
// by convention: callers must never write through Raw or Fragment, because
// every reader of that key shares the same entry.
type Entry struct {
	// Raw is the uncompressed payload.
	Raw []byte
	// Fragment is a raw deflate fragment of Raw; see the package comment.
	Fragment []byte
	// Zstd is a complete zstd frame of Raw, when the cache was filled with
	// zstd frames enabled.
	Zstd []byte
	// CRC is crc32.ChecksumIEEE(Raw), stored for splice assembly.
	CRC uint32
	// ZerosOp is x^(8·len(Raw)) modulo the CRC-32 polynomial: the GF(2)
	// operator that appends len(Raw) zero bytes, used to combine CRC into a
	// page trailer instead of re-scanning Raw (see ZerosOp).
	ZerosOp uint32
	// Digest is SHA-256 of Raw.
	Digest [32]byte
}

// ZerosOp returns the GF(2) operator x^(8·rawLen) modulo the reflected CRC-32
// polynomial in zlib's convention: combining a CRC with this operator is
// exactly what appending rawLen zero bytes to the covered data does. Entries
// store their own ZerosOp at fill time (and NewEntry derives it), so gzip
// assembly combines CRCs without re-scanning raw bytes and without computing
// the operator per request. Exported for dynamic pieces assembled but not
// cached (internal/web's loadedAt scratch).
func ZerosOp(rawLen int) uint32 { return x2nmodp(int64(rawLen), 3) }

// NewEntry returns an immutable entry for raw, fragment and zstd, copying all
// three. Fragment and Zstd are trusted to be encodings of raw; they are not
// re-verified (the caller just compressed it, and decompression on the request
// path would defeat the point of the cache). NewEntry is exported for
// per-request dynamic pieces that are assembled once but not cached; Put uses
// it for stored pieces. zstd may be nil for gzip-only pieces.
func NewEntry(raw, fragment, zstd []byte) *Entry {
	entry := &Entry{
		Raw:      append([]byte(nil), raw...),
		Fragment: append([]byte(nil), fragment...),
		Zstd:     append([]byte(nil), zstd...),
	}
	entry.Digest = sha256.Sum256(entry.Raw)
	entry.CRC = crc32.ChecksumIEEE(entry.Raw)
	entry.ZerosOp = ZerosOp(len(entry.Raw))
	return entry
}

// Cache is a byte-bounded LRU of immutable pieces, safe for concurrent use.
// It holds two key spaces over one budget and one recency order: string keys
// for content-version strings, and fixed-size digest keys for callers that
// build a content identity in place (internal/web's recorded responses).
// The zero value is not usable; call New.
type Cache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	digests map[[32]byte]*list.Element
	order   list.List
	bytes   int
	limit   int
}

// digestKeyBytes is the charged key length of a digest key, matching the
// len(key) term string keys use.
const digestKeyBytes = 32

// item is the list element payload. key and bytes live here, not on Entry, so
// an entry handed to a reader carries no cache bookkeeping. byDigest selects
// which map owns the element.
type item struct {
	key      string
	digest   [32]byte
	byDigest bool
	bytes    int
	entry    *Entry
}

// New returns a cache bounded to limit bytes. A non-positive limit disables
// storage: every Put is rejected and every Get misses, keeping the API usable
// without a nil check (CAMPFIRE_FRAGMENT_CACHE_MB=0 does the same to the
// legacy fragment cache).
func New(limit int) *Cache {
	return &Cache{entries: make(map[string]*list.Element), digests: make(map[[32]byte]*list.Element), limit: limit}
}

// Enabled reports whether the cache can store anything at all. Put still
// applies the per-entry limit/4 cap when this is true; it only distinguishes
// storage that is disabled outright, so a caller can skip work (compression,
// key building) whose result could never be retained.
func (c *Cache) Enabled() bool { return c.limit/4 > 0 }

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

// GetDigest is Get for a fixed-size content digest. Digest keys never need
// formatting or allocation, so a caller can hash its inputs in place on every
// request.
func (c *Cache) GetDigest(key [32]byte) *Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.digests[key]
	if !ok {
		return nil
	}
	c.order.MoveToFront(element)
	return element.Value.(*item).entry
}

// Put copies raw and fragment into a new immutable entry, computes the SHA-256
// digest and CRC-32 of raw, and — when it fits — publishes it under key,
// replacing any previous entry in one lock acquisition. It reports false when
// the charged size (len(key) + len(raw) + len(fragment) + entryOverhead)
// exceeds limit/4, so one piece can never thrash the whole budget; the
// returned entry is still valid and independent of the caller's buffers, it is
// simply not cached. A rejected Put changes nothing in the cache, so an
// oversized replacement leaves the previous entry in place.
//
// The copies and the digest are computed outside the mutex; only the map/list
// swap is critical. A concurrent reader therefore observes either the old
// entry or the new one, both complete — never a half-updated mix.
func (c *Cache) Put(key string, raw, fragment []byte) (*Entry, bool) {
	return c.put(item{key: key}, len(key), raw, fragment, nil)
}

// PutDigest is Put for a fixed-size content digest key, charged as
// digestKeyBytes rather than len(key).
func (c *Cache) PutDigest(key [32]byte, raw, fragment []byte) (*Entry, bool) {
	return c.put(item{digest: key, byDigest: true}, digestKeyBytes, raw, fragment, nil)
}

// PutZstd is Put for a piece that also carries a complete zstd frame; the
// frame is charged against the budget like the gzip fragment. The returned
// entry holds both encodings, so either assembles without recompression.
func (c *Cache) PutZstd(key string, raw, fragment, zstd []byte) (*Entry, bool) {
	return c.put(item{key: key}, len(key), raw, fragment, zstd)
}

// PutDigestZstd is PutZstd for a fixed-size digest key.
func (c *Cache) PutDigestZstd(key [32]byte, raw, fragment, zstd []byte) (*Entry, bool) {
	return c.put(item{digest: key, byDigest: true}, digestKeyBytes, raw, fragment, zstd)
}

func (c *Cache) put(element item, keyBytes int, raw, fragment, zstd []byte) (*Entry, bool) {
	size := keyBytes + len(raw) + len(fragment) + len(zstd) + entryOverhead
	entry := NewEntry(raw, fragment, zstd)
	if size > c.limit/4 {
		// Not cacheable, but the caller still needs the immutable piece for
		// this response; return it uncached rather than forcing a re-copy.
		return entry, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if element.byDigest {
		if previous, ok := c.digests[element.digest]; ok {
			c.bytes -= previous.Value.(*item).bytes
			c.order.Remove(previous)
		}
	} else if previous, ok := c.entries[element.key]; ok {
		c.bytes -= previous.Value.(*item).bytes
		c.order.Remove(previous)
	}
	element.bytes = size
	element.entry = entry
	if element.byDigest {
		c.digests[element.digest] = c.order.PushFront(&element)
	} else {
		c.entries[element.key] = c.order.PushFront(&element)
	}
	c.bytes += size
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
			if evicted.byDigest {
				delete(c.digests, evicted.digest)
			} else {
				delete(c.entries, evicted.key)
			}
			c.order.Remove(oldest)
		}
	}
	return entry, true
}

// LimitFromEnv returns the cache byte limit for the engine's piece cache:
// DefaultLimit when CAMPFIRE_FRAGMENT_CACHE_MB is unset, otherwise that many
// mebibytes. The key is shared with the legacy fragment cache
// (internal/web/server.go), so one deployment knob sizes both when the engine
// routes land. The recorded-response piece cache added in this tree is a
// different cache and reads its own CAMPFIRE_RECORDED_CACHE_MB; this function
// does not size it. Values outside [0, 1<<20] or non-numeric are an error,
// exactly as the legacy parser treats them. A caller must treat the error as
// startup-fatal rather than serving with an unintended cache budget.
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
