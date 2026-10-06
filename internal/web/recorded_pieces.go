package web

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/httpcompat"
	"github.com/basecamp/once-campfire-go/internal/piececache"
	"github.com/basecamp/once-campfire-go/internal/useragent"
)

// This file is the recorded-response piece path. A recorded page is stored as
// immutable raw+gzip pieces (internal/piececache) — the page shell split at its
// insertion markers, the message list as one piece — and a response is served
// from those pieces: gzip clients get one assembled multi-member buffer with a
// pre-set Content-Encoding, identity clients get the raw pieces through the
// parts channel with no copy at all. A cache hit therefore never recompresses
// the page, never copies the shell through strings.ReplaceAll, and never hashes
// the body for the ETag.
//
// CAMPFIRE_RECORDED_PIECES=off keeps the legacy HTML-fragment path
// (room_shell.go, recorded.go, fragments.go) byte for byte; the differential
// test compares the two. The one deliberate difference is the room ETag, which
// here covers the cache-stable pieces only: legacy's room ETag moves with the
// per-request loadedAt timestamp, and reproducing that would mean hashing the
// 100 KB shell on every request (see piececache.ETagOf).

// recordedSlot names the per-request value inserted at a shell cut.
type recordedSlot uint8

const (
	slotLoadedAt recordedSlot = iota // data-refresh-room-loaded-at-value
	slotMessages                     // the message-list payload
)

// recordedShell is a rendered page split at its insertion markers into stable
// pieces. count segments bound count-1 slots; when bit i of loadedMask is set,
// the slot after segments[i] is the loadedAt timestamp, otherwise it is the
// message payload.
//
// The random marker strings fall outside the segments, so the cut points are
// marker-independent: two concurrent first renders store identical pieces.
type recordedShell struct {
	segments   [3]*piececache.Entry
	count      int
	loadedMask uint8
}

// shellLayout is the byte split before compression and storage.
type shellLayout struct {
	segments [3][]byte
	slots    [2]recordedSlot
	count    int
}

// errShellUnavailable marks a shell that cannot be split into pieces; a
// tombstone under the shell's layout key keeps every later request from
// re-rendering it just to fail the split again.
var errShellUnavailable = errors.New("recorded shell: not splittable into pieces")

// recordedMessageMarker is the one-shot insertion marker the legacy recorded
// render replaces with the message list. The piece path splits the shell at
// markers instead, so a marker is only generated when a legacy render will
// actually run.
func recordedMessageMarker() template.HTML {
	return template.HTML("\x00campfire-" + rand.Text() + "\x00")
}

// recordedShellIdentity hashes exactly the page inputs the three recorded
// templates read. It replaces json.Marshal of the whole page: the append below
// is allocation-free for ordinary pages (one stack scratch) and the digest is
// the cache key itself.
//
// The field list is an audit of layout-start, room, search, messages,
// composer, optimistic and the notification help templates. Anything that
// changes rendered bytes for these routes must be appended here; a missing
// field would let two different pages share a shell. The fields that
// deliberately do not participate are the message list (its own piece), its
// marker, and the loadedAt value (inserted per request).
func recordedShellIdentity(name string, p page) [32]byte {
	var scratch [2048]byte
	buf := appendShellIdentity(scratch[:0], name, p)
	return sha256.Sum256(buf)
}

func appendShellIdentity(buf []byte, name string, p page) []byte {
	buf = appendFieldString(buf, name)

	// layout-start
	buf = appendFieldString(buf, p.Title)
	buf = appendFieldBool(buf, p.Frame)
	buf = appendFieldBool(buf, p.Reload)
	buf = appendFieldBool(buf, p.Chat)
	buf = appendFieldString(buf, p.Screen)
	buf = appendFieldString(buf, p.BodyClass)
	buf = appendFieldString(buf, p.Notice)
	buf = appendFieldString(buf, p.Error)
	buf = appendFieldString(buf, p.BackPath)
	buf = appendFieldString(buf, p.Version)
	buf = appendFieldString(buf, p.VAPIDPublicKey)
	buf = appendFieldString(buf, string(p.CustomStyles))
	buf = appendFieldInt(buf, p.User.ID)
	buf = appendFieldString(buf, p.User.Name)
	buf = appendFieldString(buf, p.User.Bio)
	buf = appendFieldInt(buf, int64(p.User.Role))
	buf = appendFieldInt(buf, int64(p.User.Status))
	buf = appendFieldTime(buf, p.User.UpdatedAt)
	buf = appendFieldBool(buf, p.Account.HasLogo)
	buf = appendFieldTime(buf, p.Account.UpdatedAt)
	buf = appendFieldInt(buf, p.Room.ID)
	buf = appendFieldString(buf, p.Room.Name)
	buf = appendFieldString(buf, p.Room.Type)
	buf = appendFieldTime(buf, p.Room.UpdatedAt)

	// room
	buf = appendFieldBool(buf, p.Invitation)
	buf = appendFieldString(buf, p.Origin)
	buf = appendFieldString(buf, p.Stream)

	// Notification help renders on the room page through layout-start.
	buf = appendPlatformIdentity(buf, p.Platform)

	// search
	buf = appendFieldString(buf, p.Query)
	buf = appendFieldInt(buf, p.ReturnRoom)
	for _, recent := range p.RecentSearches {
		buf = appendFieldString(buf, recent)
	}

	// layout-start renders len(.Messages) on the search page.
	return appendFieldInt(buf, int64(len(p.Messages)))
}

// Each field is self-delimiting: strings carry their length, numbers and times
// end with ';', booleans are one byte. With a fixed field order the
// concatenation is injective, so a changed input always changes the digest.
func appendFieldString(buf []byte, value string) []byte {
	buf = strconv.AppendInt(buf, int64(len(value)), 10)
	buf = append(buf, ':')
	return append(buf, value...)
}

func appendFieldInt(buf []byte, value int64) []byte {
	buf = strconv.AppendInt(buf, value, 10)
	return append(buf, ';')
}

func appendFieldBool(buf []byte, value bool) []byte {
	if value {
		return append(buf, '1')
	}
	return append(buf, '0')
}

func appendFieldTime(buf []byte, value time.Time) []byte {
	buf = strconv.AppendInt(buf, value.UnixNano(), 10)
	return append(buf, ';')
}

func appendPlatformIdentity(buf []byte, platform useragent.Platform) []byte {
	for _, flag := range []bool{platform.IOS, platform.Android, platform.Mac, platform.Windows, platform.Chrome, platform.Firefox, platform.Safari, platform.Edge, platform.Mobile, platform.Desktop, platform.AppleMessages} {
		buf = appendFieldBool(buf, flag)
	}
	buf = appendFieldString(buf, platform.Browser)
	return appendFieldString(buf, platform.OperatingSystem)
}

// shellSegmentKey derives one segment key from the shell identity. Only the
// miss path computes these; a hit reads them from the layout entry, so no
// hashing happens per request.
func shellSegmentKey(identity [32]byte, segment int) [32]byte {
	var buf [33]byte
	copy(buf[:], identity[:])
	buf[32] = byte(segment)
	return sha256.Sum256(buf[:])
}

// splitRecordedShell cuts a rendered page at its insertion markers. The
// message marker is required; the loadedAt marker is optional (only the room
// template renders .LoadedAt). Both must occur at most once, or the split
// cannot be reproduced from pieces.
func splitRecordedShell(rendered []byte, loadedMarker, messageMarker string) (shellLayout, error) {
	var layout shellLayout
	var cuts [2]struct {
		start, end int
		slot       recordedSlot
	}
	count := 0
	if loadedMarker != "" {
		at := bytes.Index(rendered, []byte(loadedMarker))
		if at >= 0 {
			if bytes.Count(rendered, []byte(loadedMarker)) != 1 {
				return layout, errors.New("recorded shell: loadedAt marker repeated")
			}
			cuts[count] = struct {
				start, end int
				slot       recordedSlot
			}{at, at + len(loadedMarker), slotLoadedAt}
			count++
		}
	}
	at := bytes.Index(rendered, []byte(messageMarker))
	if at < 0 {
		return layout, errors.New("recorded shell: message marker missing")
	}
	if bytes.Count(rendered, []byte(messageMarker)) != 1 {
		return layout, errors.New("recorded shell: message marker repeated")
	}
	cuts[count] = struct {
		start, end int
		slot       recordedSlot
	}{at, at + len(messageMarker), slotMessages}
	count++
	if count == 2 && cuts[0].start > cuts[1].start {
		cuts[0], cuts[1] = cuts[1], cuts[0]
	}
	start := 0
	for i := 0; i < count; i++ {
		layout.segments[i] = rendered[start:cuts[i].start]
		layout.slots[i] = cuts[i].slot
		start = cuts[i].end
	}
	layout.segments[count] = rendered[start:]
	layout.count = count + 1
	return layout, nil
}

// shellPieces returns the stable pieces for a page shell, rendering and storing
// them on a miss. Pieces are keyed by content, so a miss is always a cold page,
// never a stale one. needMember asks for gzip members because this request will
// assemble a gzip body; when the cache cannot store anything and no gzip body
// is being assembled, the members would be discarded immediately, so segment
// compression is skipped.
func (s *Server) shellPieces(p page, name string, needMember bool) (recordedShell, error) {
	identity := recordedShellIdentity(name, p)
	switch shell, status := s.loadShell(identity); status {
	case shellHit:
		return shell, nil
	case shellUnavailable:
		return recordedShell{}, errShellUnavailable
	}
	messageMarker := "\x00campfire-" + rand.Text() + "\x00"
	loadedMarker := "campfire-loaded-" + rand.Text()
	// p.Messages stays populated: the search page renders len(.Messages).
	p.MessagesHTML, p.LoadedAt = template.HTML(messageMarker), loadedMarker
	b := borrowBuffer()
	defer releaseBuffer(b)
	if err := s.templates.ExecuteTemplate(b, name, p); err != nil {
		return recordedShell{}, err
	}
	layout, err := splitRecordedShell(b.Bytes(), loadedMarker, messageMarker)
	if err != nil {
		// A tombstone prevents the next request from rendering the shell only
		// to fail the same split; the caller serves the legacy render instead.
		s.pieces.PutDigest(identity, []byte{0}, nil)
		return recordedShell{}, err
	}
	var shell recordedShell
	shell.count = layout.count
	for i := 0; i < layout.count-1; i++ {
		if layout.slots[i] == slotLoadedAt {
			shell.loadedMask |= 1 << i
		}
	}
	// The layout entry names its segments so a hit never re-hashes.
	manifest := make([]byte, 2+32*layout.count)
	manifest[0], manifest[1] = byte(layout.count), shell.loadedMask
	for i := 0; i < layout.count; i++ {
		key := shellSegmentKey(identity, i)
		copy(manifest[2+32*i:], key[:])
		var member []byte
		if needMember || s.pieces.Enabled() {
			member = compressGzip(layout.segments[i])
		}
		entry, _ := s.pieces.PutDigest(key, layout.segments[i], member)
		shell.segments[i] = entry
	}
	s.pieces.PutDigest(identity, manifest, nil)
	return shell, nil
}

// shellStatus is loadShell's tri-state result.
type shellStatus uint8

const (
	shellMiss shellStatus = iota
	shellHit
	shellUnavailable
)

// loadShell rebuilds a shell from the cache. A missing layout or segment is a
// miss: the caller renders and re-stores. A one-byte layout is the tombstone
// left by a failed split; a layout whose manifest is malformed is a miss too.
func (s *Server) loadShell(identity [32]byte) (recordedShell, shellStatus) {
	manifest := s.pieces.GetDigest(identity)
	if manifest == nil {
		return recordedShell{}, shellMiss
	}
	if len(manifest.Raw) == 1 {
		return recordedShell{}, shellUnavailable
	}
	if len(manifest.Raw) < 2 {
		return recordedShell{}, shellMiss
	}
	count, mask := int(manifest.Raw[0]), manifest.Raw[1]
	if count < 2 || count > 3 || mask>>(count-1) != 0 || len(manifest.Raw) != 2+32*count {
		return recordedShell{}, shellMiss
	}
	shell := recordedShell{count: count, loadedMask: mask}
	for i := 0; i < count; i++ {
		var key [32]byte
		copy(key[:], manifest.Raw[2+32*i:])
		entry := s.pieces.GetDigest(key)
		if entry == nil {
			return recordedShell{}, shellMiss
		}
		shell.segments[i] = entry
	}
	return shell, shellHit
}

// writeRecordedPieces serves a recorded response from cached pieces. It returns
// handled=false, before writing anything, when the page shell cannot be split
// into pieces; the caller then renders the legacy way.
//
// Order of operations is the 304 contract: the ETag covers the cache-stable
// pieces only, and a matching If-None-Match returns before any assembly. A
// gzip client gets one assembled multi-member buffer and a pre-set
// Content-Encoding that front.Deflate passes through; an identity client gets
// the pieces' raw bytes through the parts channel, which is already the wire
// form and copies nothing. HEAD requests skip assembly and report the length
// the GET body would have.
func (s *Server) writeRecordedPieces(w http.ResponseWriter, r *http.Request, status int, name string, p page, recorded recordedPayload) (bool, error) {
	payload := recorded.piece
	if payload == nil {
		s.recordedShellFallbacks.Add(1)
		return false, nil
	}
	gzipped := clientAcceptsGzip(r)
	shell, err := s.shellPieces(p, name, gzipped)
	if err != nil {
		s.recordedShellFallbacks.Add(1)
		if s.recordedShellWarned.CompareAndSwap(false, true) {
			slog.Warn("recorded response served by the legacy renderer", "name", name, "error", err)
		}
		return false, nil
	}

	// Stable pieces in output order, minus the per-request loadedAt, are what
	// the validator covers (piececache.ETagOf).
	var stableBuf [5]*piececache.Entry
	stable := stableBuf[:0]
	for i := 0; i < shell.count; i++ {
		stable = append(stable, shell.segments[i])
		if i < shell.count-1 && shell.loadedMask&(1<<i) == 0 {
			stable = append(stable, payload)
		}
	}
	h := w.Header()
	if h.Get("ETag") == "" {
		digest, err := piececache.ETagOf(stable...)
		if err != nil {
			return true, err
		}
		h.Set("ETag", weakETag(digest))
	}
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "max-age=0, private, must-revalidate")
	}
	if notModified(w, r, h.Get("ETag"), time.Time{}) {
		return true, nil
	}
	buffered := findResponseBuffer(w)

	if gzipped {
		var loaded *piececache.Entry
		if shell.loadedMask != 0 {
			var release func()
			loaded, release = borrowLoadedAt(p.LoadedAt)
			defer release()
		}
		var partsBuf [5]*piececache.Entry
		parts := partsBuf[:0]
		for i := 0; i < shell.count; i++ {
			parts = append(parts, shell.segments[i])
			if i >= shell.count-1 {
				continue
			}
			if shell.loadedMask&(1<<i) != 0 {
				parts = append(parts, loaded)
			} else {
				parts = append(parts, payload)
			}
		}
		if r.Method == "HEAD" {
			total := 0
			for _, part := range parts {
				total += len(part.Member)
			}
			h.Set("Content-Encoding", "gzip")
			addVaryAcceptEncoding(h)
			w.WriteHeader(status)
			if sw, ok := w.(*sessionWriter); ok && sw.failed {
				return true, nil
			}
			if buffered != nil {
				buffered.encodedLength = total
			}
			return true, nil
		}
		var assembly *recordedAssemblyBuffer
		var dst []byte
		if buffered != nil {
			assembly = borrowAssemblyBuffer()
			dst = assembly.buf[:0]
		}
		encoded, _, err := piececache.Assemble(dst, piececache.Gzip, parts...)
		if err != nil {
			assembly.release()
			return true, err
		}
		if assembly != nil {
			assembly.buf = encoded
		}
		s.recordedAssemblies.Add(1)
		h.Set("Content-Encoding", "gzip")
		addVaryAcceptEncoding(h)
		w.WriteHeader(status)
		if sw, ok := w.(*sessionWriter); ok && sw.failed {
			assembly.release()
			return true, nil
		}
		if buffered == nil {
			_, err = w.Write(encoded)
			assembly.release()
			return true, err
		}
		buffered.encoded = encoded
		buffered.assembly = assembly
		return true, nil
	}

	// Identity: the pieces' raw bytes are already the wire form. Serving them
	// through the parts channel keeps the response zero-copy, exactly as the
	// legacy recorded path did; only the gzip form needs an assembly buffer.
	var rawBuf [5][]byte
	raws := rawBuf[:0]
	for i := 0; i < shell.count; i++ {
		raws = append(raws, shell.segments[i].Raw)
		if i >= shell.count-1 {
			continue
		}
		if shell.loadedMask&(1<<i) != 0 {
			raws = append(raws, []byte(p.LoadedAt))
		} else {
			raws = append(raws, payload.Raw)
		}
	}
	addVaryAcceptEncoding(h)
	w.WriteHeader(status)
	if sw, ok := w.(*sessionWriter); ok && sw.failed {
		return true, nil
	}
	if r.Method == "HEAD" {
		if buffered != nil {
			total := 0
			for _, raw := range raws {
				total += len(raw)
			}
			buffered.encodedLength = total
		}
		return true, nil
	}
	if buffered == nil {
		for _, raw := range raws {
			if _, err := w.Write(raw); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	buffered.parts = raws
	return true, nil
}

// findResponseBuffer returns the per-request responseBuffer behind w's wrapper
// chain, or nil when the response is not buffered.
func findResponseBuffer(w http.ResponseWriter) *responseBuffer {
	for {
		if buffered, ok := w.(*responseBuffer); ok {
			return buffered
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = wrapper.Unwrap()
	}
}

// weakETag spells the validator the way Rack::ETag does: W/"<low 16 bytes>".
func weakETag(digest [32]byte) string {
	return `W/"` + hex.EncodeToString(digest[:16]) + `"`
}

// addVaryAcceptEncoding mirrors front.addVary: append the token unless it (or a
// wildcard) is already present in any Vary value.
func addVaryAcceptEncoding(h http.Header) {
	for _, line := range h.Values("Vary") {
		for _, value := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(value), "Accept-Encoding") || strings.TrimSpace(value) == "*" {
				return
			}
		}
	}
	if old := h.Get("Vary"); old != "" {
		h.Set("Vary", old+",Accept-Encoding")
	} else {
		h.Set("Vary", "Accept-Encoding")
	}
}

// clientAcceptsGzip reports whether this request may receive a pre-encoded gzip
// body. It defers to httpcompat.Encoding, the same selector front.Deflate uses
// for its own negotiation, so a pre-encoded body is only sent when the
// middleware would itself have chosen gzip (front.Deflate passes a pre-set
// Content-Encoding through untouched, including its 406 policy). When front is
// not in the chain, at worst the response goes out as identity bytes and the
// caller compresses them.
func clientAcceptsGzip(r *http.Request) bool {
	return httpcompat.Encoding(r.Header.Get("Accept-Encoding")) == "gzip"
}

// recordedCompressor is one pooled gzip writer plus the buffer it fills. The
// buffer is capped like the response buffer pool, so one miss-compressed page
// cannot pin an unbounded buffer per P.
type recordedCompressor struct {
	writer *gzip.Writer
	buf    bytes.Buffer
}

var recordedCompressors = sync.Pool{New: func() any {
	writer, _ := gzip.NewWriterLevel(nil, 6)
	return &recordedCompressor{writer: writer}
}}

// compressGzip returns a complete gzip member of raw. It is used when a piece
// is stored; the writer and buffer are reset before use and the returned bytes
// are an independent copy, so a pooled buffer can never leak one caller's
// bytes into another's response.
func compressGzip(raw []byte) []byte { return compressGzipInto(nil, raw) }

// compressGzipInto appends a gzip member of raw to dst, reusing the pooled
// writer. dst may alias the caller's own buffer; the member is fully rewritten
// from index len(dst) onwards.
func compressGzipInto(dst, raw []byte) []byte {
	c := recordedCompressors.Get().(*recordedCompressor)
	c.buf.Reset()
	c.writer.Reset(&c.buf)
	c.writer.Header.OS = 3
	_, _ = c.writer.Write(raw)
	_ = c.writer.Close()
	c.writer.Reset(nil)
	dst = append(dst, c.buf.Bytes()...)
	if c.buf.Cap() <= 1<<20 {
		recordedCompressors.Put(c)
	}
	return dst
}

// recordedAssemblyLimit caps the buffers the assembly pool retains. A page
// whose gzip members exceed it is still served from a fresh allocation, it is
// simply not pooled.
const recordedAssemblyLimit = 1 << 20

// recordedAssemblyBuffer is one pooled buffer for the assembled gzip body.
// Ownership belongs to a single responseBuffer from borrow to finish, and
// Assemble writes the served bytes from index zero, so a reused buffer is
// always fully overwritten for its length — the poisoning test in
// recorded_pieces_test.go is the proof. Pooling a pointer keeps Put free of
// the boxing allocation a bare slice would cost.
type recordedAssemblyBuffer struct {
	buf []byte
}

var recordedAssemblyBuffers = sync.Pool{New: func() any {
	return &recordedAssemblyBuffer{buf: make([]byte, 0, 64<<10)}
}}

func borrowAssemblyBuffer() *recordedAssemblyBuffer {
	return recordedAssemblyBuffers.Get().(*recordedAssemblyBuffer)
}

// release returns the buffer to the pool. Oversized buffers are dropped so a
// single huge page cannot pin memory per P.
func (a *recordedAssemblyBuffer) release() {
	if a == nil {
		return
	}
	if cap(a.buf) > recordedAssemblyLimit {
		a.buf = nil
		return
	}
	a.buf = a.buf[:0]
	recordedAssemblyBuffers.Put(a)
}

// loadedAtScratch caches one (timestamp value -> gzip member) conversion per
// pooled scratch: the room page's loadedAt changes at most once per
// millisecond, so a burst of requests reuses one member. Comparing the value
// makes a stale pooled member impossible. Digest is deliberately zero: the
// loadedAt slot is excluded from the response ETag, and only the assembled
// bytes ever read the piece.
type loadedAtScratch struct {
	value  string
	raw    []byte
	member []byte
	entry  piececache.Entry
}

var loadedAtScratches = sync.Pool{New: func() any { return &loadedAtScratch{} }}

// borrowLoadedAt returns a dynamic gzip piece for value and the release that
// returns the scratch to the pool. The piece is valid until release.
func borrowLoadedAt(value string) (*piececache.Entry, func()) {
	scratch := loadedAtScratches.Get().(*loadedAtScratch)
	if scratch.member == nil || scratch.value != value {
		scratch.raw = append(scratch.raw[:0], value...)
		scratch.member = compressGzipInto(scratch.member[:0], scratch.raw)
		scratch.value = value
	}
	scratch.entry = piececache.Entry{Raw: scratch.raw, Member: scratch.member}
	return &scratch.entry, func() { loadedAtScratches.Put(scratch) }
}
