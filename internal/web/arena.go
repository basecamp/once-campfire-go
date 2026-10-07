package web

import (
	"net/http"
	"sync"
)

// This file is the ENGINE-48 per-request reclaimable storage: one bump-
// allocated byte block per request (the arena) from which the recorded
// response's data — parts, header scratch, the assembled gzip/zstd body — is
// carved, plus ownership-disciplined pools for the per-request writer structs
// (responseBuffer, sessionWriter, browserSession).
//
// The split is deliberate: Go's GC does not scan byte-slice backing arrays for
// pointers, so a struct containing pointers (a responseBuffer, a
// browserSession's map) cannot live in arena bytes without its fields being
// invisible to the collector. The arena therefore holds pointer-free byte
// data only, and the three writer structs come from pools whose ownership is
// exactly one request (borrow at request start, put back after finish) —
// the same pooled-pointer discipline recordedAssemblyBuffers already uses.
// Both paths reclaim instead of freeing, so a warmed request allocates
// nothing.
//
// The pools follow the repo's sync.Pool contract: every borrow fully
// re-initializes the value before any reader sees it, and the poisoning test
// in arena_test.go pins the overwrite contract. The arena's release fills the
// used range with 0xAA under test, so a carve that reads a byte it did not
// write sees the marker instead of a previous request's bytes.
//
// One documented fallback: a carve larger than the block's growth cap (a page
// whose assembled body or head exceeds recordedAssemblyLimit) returns nil and
// the caller falls back to its previous pooled or fresh allocation,
// byte-identically.

// arenaLimit caps the block retained at release, matching the recorded
// assembly pool's cap: a page whose carved bytes exceed it is still served,
// its block is simply dropped rather than pooled.
const arenaLimit = 1 << 20

// arenaInitial is the first allocation of a fresh block. Carves grow the
// block geometrically up to arenaLimit.
const arenaInitial = 64 << 10

// requestArena is one per-request bump allocator over one byte block. It is
// not safe for concurrent use; one request owns it and carves serially.
type requestArena struct {
	buf    []byte
	off    int
	water  int  // high-water mark of bytes handed out, for the poison test
	poison bool // test-only: fill the used range with 0xAA at release
}

var requestArenas = sync.Pool{New: func() any {
	return &requestArena{buf: make([]byte, 0, arenaInitial)}
}}

// borrowRequestArena takes a block for one request. The caller must pair it
// with releaseRequestArena exactly once, after the response has been fully
// written (finish and emission), because carves alias the block.
func borrowRequestArena() *requestArena {
	return requestArenas.Get().(*requestArena)
}

// releaseRequestArena resets the block without freeing it: carves are dead,
// and the block returns to the pool for the next request. With a.poison set
// (tests), the used range is filled with 0xAA so a carve that reads a byte it
// did not write sees the marker instead of a previous request's bytes.
func releaseRequestArena(a *requestArena) {
	if a == nil {
		return
	}
	a.poisonAndReset()
	requestArenas.Put(a)
}

// poisonAndReset fills the handed-out range with the poison byte and resets
// the block for its next owner. It is the overwrite contract of the arena:
// a carve that reads a byte it did not write sees 0xAA, never another
// request's data. Released as an internal hook so tests can verify the
// contract without contending with the pool.
func (a *requestArena) poisonAndReset() {
	if a.poison {
		used := max(a.water, a.off)
		block := a.buf[:cap(a.buf)] // the backing array, fully addressable
		if used > len(block) {
			used = len(block)
		}
		for i := 0; i < used; i++ {
			block[i] = 0xAA
		}
	}
	if cap(a.buf) > arenaLimit {
		a.buf = nil
	}
	a.buf = a.buf[:0]
	a.off, a.water = 0, 0
}

// carveBlock returns a zero-length slice backed by the arena with at least n
// bytes of capacity, or nil when the block cannot provide them (the caller
// falls back to its pooled path). Appending into the returned slice writes
// arena memory up to the block's remaining capacity and never grows into a
// fresh allocation, as long as the total bytes appended stay under n. The
// carve is uninitialized: it must be fully written before it is read — the
// release poisoning makes a violation visible in tests rather than serving
// another request's data.
func (a *requestArena) carveBlock(n int) []byte {
	if n > arenaLimit {
		return nil
	}
	if cap(a.buf)-a.off < n {
		grow := cap(a.buf) * 2
		if grow < a.off+n {
			grow = a.off + n
		}
		if grow > arenaLimit {
			grow = arenaLimit
		}
		if grow-a.off < n {
			return nil
		}
		next := make([]byte, len(a.buf), grow)
		copy(next, a.buf)
		a.buf = next
	}
	s := a.buf[a.off:a.off:cap(a.buf)]
	a.off += n
	if a.off > a.water {
		a.water = a.off
	}
	return s
}

// carve returns n initialized-to-zero bytes, for parts whose shape is a fixed
// slice. The bytes are zeroed because a parts descriptor is read by writers
// before every element is overwritten; zeroing makes the contract total at a
// cost paid once per request. (carveBlock is the hot carve: it hands out
// appendable room and callers write it fully before any reader sees it.)
func (a *requestArena) carve(n int) []byte {
	if n > arenaLimit {
		return nil
	}
	if cap(a.buf)-a.off < n {
		grow := cap(a.buf) * 2
		if grow < a.off+n {
			grow = a.off + n
		}
		if grow > arenaLimit {
			grow = arenaLimit
		}
		if grow-a.off < n {
			return nil
		}
		next := make([]byte, len(a.buf), grow)
		copy(next, a.buf)
		a.buf = next
	}
	s := a.buf[a.off : a.off+n : a.off+n]
	a.off += n
	if a.off > a.water {
		a.water = a.off
	}
	for i := range s {
		s[i] = 0
	}
	return s
}

// ---------------------------------------------------------------------------
// Per-request writer struct pools (ownership: exactly one request).

var responseBufferPool = sync.Pool{New: func() any { return &responseBuffer{} }}

// borrowResponseBuffer takes a response buffer for one request and resets it
// for fresh use: all mutable state lives in the embedded ResponseWriter and
// the request arena, so the reset is the struct's zero fields.
func borrowResponseBuffer(w http.ResponseWriter, arena *requestArena) *responseBuffer {
	b := responseBufferPool.Get().(*responseBuffer)
	b.ResponseWriter = w
	b.arena = arena
	b.status, b.exception = 0, false
	b.body = nil
	b.parts = nil
	b.encoded = nil
	b.assembly = nil
	b.encodedLength = 0
	return b
}

func releaseResponseBuffer(b *responseBuffer) {
	if b == nil {
		return
	}
	b.ResponseWriter = nil
	b.arena = nil
	b.framed = false
	b.recordedHead = nil
	b.precomposed = false
	b.recordedEtag = nil
	b.recordedStatus = 0
	responseBufferPool.Put(b)
}

var sessionWriterPool = sync.Pool{New: func() any { return &sessionWriter{} }}

// borrowSessionWriter takes the session writer for one request; allocBrowser
// state is pooled alongside it (browserSession values live in the request and
// the pool, never between requests — see releaseBrowserSession).
func borrowSessionWriter(w http.ResponseWriter, state *browserSession) *sessionWriter {
	sw := sessionWriterPool.Get().(*sessionWriter)
	sw.ResponseWriter = w
	sw.session = state
	sw.written, sw.failed = false, false
	return sw
}

func releaseSessionWriter(sw *sessionWriter) {
	if sw == nil {
		return
	}
	sw.ResponseWriter = nil
	sw.session = nil
	sessionWriterPool.Put(sw)
}

var browserSessionPool = sync.Pool{New: func() any { return &browserSession{} }}

// borrowBrowserSession takes the browser-session state for one request. The
// values map is reset to nil here and allocated on first load, so a request
// that never touches the session (the recorded hit path) allocates nothing.
func borrowBrowserSession(s *Server, r *http.Request) *browserSession {
	st := browserSessionPool.Get().(*browserSession)
	st.server = s
	st.request = r
	st.loaded, st.changed = false, false
	st.values = nil
	return st
}

func releaseBrowserSession(st *browserSession) {
	if st == nil {
		return
	}
	st.server = nil
	st.request = nil
	st.values = nil
	browserSessionPool.Put(st)
}

// partBytes returns request-lifetime bytes for the dynamic loadedAt slot of
// a recorded identity response: arena memory when the block can serve it
// (zero allocation, released with the request after finish), else a copy.
// The returned slice must not outlive finish.
func (w *responseBuffer) partBytes(value string) []byte {
	if w.arena != nil {
		if b := w.arena.carve(len(value)); b != nil {
			copy(b, value)
			return b
		}
	}
	return []byte(value)
}
