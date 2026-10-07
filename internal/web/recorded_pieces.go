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
	"github.com/basecamp/once-campfire-go/internal/zstd"
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
// message payload. identity is the content digest of the page inputs (the
// shell's cache key), kept on the shell so the response path can derive
// per-variant keys without re-hashing.
type recordedShell struct {
	segments   [3]*piececache.Entry
	count      int
	loadedMask uint8
	identity   [32]byte
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
// never a stale one. needGzip asks for gzip members because this request will
// assemble a gzip body; needZstd asks for zstd frames (ENGINE-50). When the
// cache cannot store anything and no compressed body is being assembled, the
// members would be discarded immediately, so compression is skipped; the two
// booleans let the caller request exactly the members it will use, so the
// first fill of a page for one encoding does not compress the other member
// needlessly.
func (s *Server) shellPieces(p page, name string, needGzip, needZstd bool) (recordedShell, error) {
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
	shell.identity = identity
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
		var member, zstdMember []byte
		if needGzip || s.pieces.Enabled() {
			member = compressGzip(layout.segments[i])
		}
		if s.zstdPieces && (needZstd || s.pieces.Enabled()) {
			zstdMember = compressZstd(layout.segments[i])
		}
		entry, _ := s.pieces.PutDigestZstd(key, layout.segments[i], member, zstdMember)
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
	shell := recordedShell{count: count, loadedMask: mask, identity: identity}
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
// into pieces; the caller then renders the legacy way. encoding is the
// request's encoding decision ("gzip", "zstd" or "identity"), computed once by
// the caller so this path and recordedMessageList agree without a second
// negotiation.
//
// Order of operations is the 304 contract: the ETag covers the cache-stable
// pieces only, and a matching If-None-Match returns before any assembly. A
// compressed client gets one assembled multi-member buffer (gzip members or
// zstd frames) and a pre-set Content-Encoding that front.Deflate passes
// through; an identity client gets the pieces' raw bytes through the parts
// channel, which is already the wire form and copies nothing. HEAD requests
// skip assembly and report the length the GET body would have.
//
// When the precomposed-framing path applies (see framing.go), the response is
// emitted as one head block plus the parts, with no http.Header map churn and
// no per-request header strings; every other path is byte-identical to the
// map path.
func (s *Server) writeRecordedPieces(w http.ResponseWriter, r *http.Request, status int, name string, p page, recorded recordedPayload, encoding string) (bool, error) {
	payload := recorded.piece
	if payload == nil {
		s.recordedShellFallbacks.Add(1)
		return false, nil
	}
	needGzip := encoding == "gzip"
	needZstd := s.zstdPieces && encoding == "zstd"
	shell, err := s.shellPieces(p, name, needGzip, needZstd)
	if err != nil {
		s.recordedShellFallbacks.Add(1)
		if s.recordedShellWarned.CompareAndSwap(false, true) {
			slog.Warn("recorded response served by the legacy renderer", "name", name, "error", err)
		}
		return false, nil
	}
	// A cache filled before zstd members were enabled holds pieces without
	// zstd frames; degrade such requests to gzip (whose members every piece
	// carries) rather than assembling a zstd body from nothing.
	if encoding == "zstd" && !piecesCarryZstd(shell, payload) {
		encoding = "gzip"
		needGzip = true
		needZstd = false
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
	// The response buffer gate: framing needs its arena for the head and
	// body carves, and the buffer carries the writer-side gate from
	// ServeHTTP (buffered.framed).
	buffered := findResponseBuffer(w)
	// The variant cache hands back the identity-record digest for this
	// (shell, payload, encoding) combination; a miss computes it once (the
	// digest pass over the stable pieces) and stores it under the variant
	// key, keyed by the same content digests the pieces use, so a hit is
	// correct by construction and the next request skips the digest pass.
	// The ETag spelling is only produced when it is consumed: a framed
	// request renders it into arena memory (request-lifetime), the map path
	// renders the usual heap string. Neither path writes the map unless the
	// map path runs.
	encodingTag := encodeIdentity
	wireEncoding := ""
	switch encoding {
	case "gzip":
		encodingTag, wireEncoding = encodeGzip, "gzip"
	case "zstd":
		encodingTag, wireEncoding = encodeZstd, "zstd"
	}
	var etagDigest *[32]byte
	if h.Get("ETag") == "" {
		variantKey := variantKeyDigest(shell.identity, payload.Digest, encodingTag)
		if variant := s.pieces.GetDigest(variantKey); variant != nil && len(variant.Raw) == 32 {
			digest := *(*[32]byte)(variant.Raw)
			etagDigest = &digest
		} else {
			digest, err := piececache.ETagOf(stable...)
			if err != nil {
				return true, err
			}
			s.pieces.PutDigest(variantKey, digest[:], nil)
			etagDigest = &digest
		}
	}
	// The precomposed/request gates settle first: the writer gate and the
	// precomposed/arena flags were captured on the buffer by ServeHTTP, and
	// the request-side gates (a changed browser session or a Set-Cookie mean
	// the response head is not the recorded block) are evaluated here. A
	// fresh request (If-None-Match) on this path emits its 304 through the
	// same precomposed block, with the entity headers suppressed by the
	// status; the map path writes the 304 immediately as before.
	compressed := wireEncoding != ""
	framed := buffered != nil && buffered.framed && status == 200 && encoding != "" && r.Method != "HEAD"
	if framed {
		var state *browserSession
		if raw, ok := r.Context().Value(browserSessionKey{}).(*browserSession); ok {
			state = raw
		}
		if !precomposedEligible(r, h, state) {
			framed = false
		}
	}
	// The ETag spelling is produced only where it is consumed: a framed
	// request renders it into arena memory (bytes valid until the arena is
	// released after finish) and compares it against If-None-Match without a
	// string conversion; the map path renders the usual heap string it
	// places in the map. The decision is made after the final gates settle,
	// so a request that falls back to the map path always has its string.
	etagString := ""
	var etagBytes []byte
	if etagDigest != nil {
		if framed && buffered != nil && buffered.arena != nil {
			if scratch := buffered.arena.carveBlock(40); scratch != nil {
				etagBytes = weakETagInto(scratch, *etagDigest)
				framed = true
			}
		}
		if etagBytes == nil {
			// The arena could not hold the validator spelling: fall to the
			// map path (which renders the heap string) rather than framing
			// a head with an empty ETag.
			framed = false
			etagString = weakETag(*etagDigest)
		}
	} else {
		etagString = h.Get("ETag")
		if framed {
			etagBytes = []byte(etagString)
		}
	}
	// A pre-set ETag (the messages route's validator) is converted to bytes
	// only when this request actually frames.
	if framed && etagBytes == nil {
		// The arena could not hold the validator spelling (or a pre-set
		// validator produced nothing): not safe to frame an empty ETag.
		framed = false
	}
	if framed && requestIsFresh(r, etagBytes, time.Time{}) {
		// The 304 emission happens at finish from the same precomposed
		// block (entity headers suppressed); mark the variant and return
		// without writing. A block carve failure falls through to the map
		// path, whose notModified writes the 304 immediately with the
		// restored headers.
		if block := buffered.arena.carveBlock(2048); block != nil {
			buffered.recordedHead = s.appendRecordedHead(block, etagBytes, "", "", 0, http.StatusNotModified)
			buffered.precomposed = true
			buffered.recordedEtag = etagBytes
			buffered.recordedStatus = http.StatusNotModified
			if sw, ok := w.(*sessionWriter); ok {
				sw.written = true
			} else {
				buffered.status = http.StatusNotModified
			}
			return true, nil
		}
	}
	if framed {
		ok := true
		if compressed {
			var loaded *piececache.Entry
			if shell.loadedMask != 0 {
				scratch := borrowLoadedAt(p.LoadedAt, encoding == "zstd")
				defer scratch.release()
				loaded = &scratch.entry
			}
			var partsBuf [5]*piececache.Entry
			parts := partsBuf[:0]
			total := 0
			// The carve room must cover the assembled members exactly: the
			// slot members (loadedAt or the payload) are part of the body,
			// so a total that omitted them would let the assembly write past
			// the carve into the next carve's memory.
			for i := 0; i < shell.count; i++ {
				parts = append(parts, shell.segments[i])
				total += memberLen(shell.segments[i], encoding)
				if i >= shell.count-1 {
					continue
				}
				if shell.loadedMask&(1<<i) != 0 {
					parts = append(parts, loaded)
					total += memberLen(loaded, encoding)
				} else {
					parts = append(parts, payload)
					total += memberLen(payload, encoding)
				}
			}
			dst := buffered.arena.carveBlock(total)
			if dst == nil {
				ok = false
			} else {
				var pieceEncoding piececache.Encoding
				if encoding == "zstd" {
					pieceEncoding = piececache.Zstd
				} else {
					pieceEncoding = piececache.Gzip
				}
				encoded, _, err := piececache.Assemble(dst, pieceEncoding, parts...)
				if err != nil {
					ok = false
				} else {
					buffered.encoded = encoded
				}
			}
		} else {
			var raws [][]byte
			raws = buffered.partsBuf[:0]
			for i := 0; i < shell.count; i++ {
				raws = append(raws, shell.segments[i].Raw)
				if i >= shell.count-1 {
					continue
				}
				if shell.loadedMask&(1<<i) != 0 {
					raws = append(raws, buffered.partBytes(p.LoadedAt))
				} else {
					raws = append(raws, payload.Raw)
				}
			}
			buffered.parts = raws
		}
		if ok {
			length := 0
			if len(buffered.encoded) > 0 {
				length = len(buffered.encoded)
			} else {
				for _, raw := range buffered.parts {
					length += len(raw)
				}
			}
			// The head block is ~700 bytes with the longest process
			// constants, so a 2048-byte carve guarantees the appends stay
			// inside the arena block.
			block := buffered.arena.carveBlock(2048)
			if block != nil {
				buffered.recordedHead = s.appendRecordedHead(block, etagBytes, h.Get("Last-Modified"), wireEncoding, length, http.StatusOK)
				buffered.precomposed = true
				buffered.recordedEtag = etagBytes
				buffered.recordedStatus = http.StatusOK
				if sw, ok := w.(*sessionWriter); ok {
					// The session writer's WriteHeader (and the deferred
					// wrapper in ServeHTTP) must not emit a second head; the
					// session was checked unchanged, so commit is a no-op.
					sw.written = true
				} else {
					buffered.status = http.StatusOK
				}
				return true, nil
			}
		}
		// The arena could not serve the body or head: fall through to the
		// map path below, which needs the fixed headers the framed request
		// skipped.
		s.restoreRecordedHeaders(h)
	}
	// A request whose writer gate passed but that did not take the framed
	// path (session changed, Set-Cookie, a failed carve) skipped the fixed
	// headers in ServeHTTP and render; restore them so the map-path response
	// is byte-identical to a request that never framed.
	if buffered != nil && buffered.framed {
		s.restoreRecordedHeaders(h)
	}
	if notModified(w, r, etagString, time.Time{}) {
		return true, nil
	}
	if h.Get("ETag") == "" && etagString != "" {
		h.Set("ETag", etagString)
	}
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "max-age=0, private, must-revalidate")
	}

	if compressed {
		var loaded *piececache.Entry
		if shell.loadedMask != 0 {
			scratch := borrowLoadedAt(p.LoadedAt, encoding == "zstd")
			defer scratch.release()
			loaded = &scratch.entry
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
				total += memberLen(part, encoding)
			}
			h.Set("Content-Encoding", wireEncoding)
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
		var pieceEncoding piececache.Encoding
		if encoding == "zstd" {
			pieceEncoding = piececache.Zstd
		} else {
			pieceEncoding = piececache.Gzip
		}
		encoded, _, err := piececache.Assemble(dst, pieceEncoding, parts...)
		if err != nil {
			assembly.release()
			return true, err
		}
		if assembly != nil {
			assembly.buf = encoded
		}
		s.recordedAssemblies.Add(1)
		h.Set("Content-Encoding", wireEncoding)
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
	var raws [][]byte
	if buffered != nil {
		raws = buffered.partsBuf[:0]
	} else {
		var stack [5][]byte
		raws = stack[:0]
	}
	for i := 0; i < shell.count; i++ {
		raws = append(raws, shell.segments[i].Raw)
		if i >= shell.count-1 {
			continue
		}
		if shell.loadedMask&(1<<i) != 0 {
			raws = append(raws, buffered.partBytes(p.LoadedAt))
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

// memberLen returns the wire length of one part under the chosen encoding.
func memberLen(part *piececache.Entry, encoding string) int {
	switch encoding {
	case "zstd":
		return len(part.Zstd)
	case "gzip":
		return len(part.Member)
	default:
		return len(part.Raw)
	}
}

// piecesCarryZstd reports whether every cache-stable piece of this response
// has a zstd frame, so a zstd body can be assembled from them.
func piecesCarryZstd(shell recordedShell, payload *piececache.Entry) bool {
	for i := 0; i < shell.count; i++ {
		segment := shell.segments[i]
		if len(segment.Raw) > 0 && len(segment.Zstd) == 0 {
			return false
		}
	}
	return len(payload.Raw) == 0 || len(payload.Zstd) > 0
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
// The hex digits go into a stack buffer; the returned string is the one the
// header map needs anyway.
func weakETag(digest [32]byte) string {
	var buf [3 + 32 + 1]byte
	copy(buf[:3], `W/"`)
	hex.Encode(buf[3:35], digest[:16])
	buf[35] = '"'
	return string(buf[:])
}

// weakETagInto renders the weak validator spelling of digest into dst and
// returns the extended slice. The framed path renders it into arena memory,
// so the ETag costs no heap string on the precomposed path.
func weakETagInto(dst []byte, digest [32]byte) []byte {
	dst = append(dst, `W/"`...)
	const hexDigits = "0123456789abcdef"
	for _, b := range digest[:16] {
		dst = append(dst, hexDigits[b>>4], hexDigits[b&0xF])
	}
	return append(dst, '"')
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

// clientEncoding selects the wire encoding for this request's pre-encoded
// recorded response: "gzip", "zstd" or "identity", or "" when the client
// accepts nothing (the front middleware answers 406 on the identity path).
// When zstd members are disabled the selector returns exactly what
// httpcompat.Encoding returns (clientAcceptsGzip's result).
func (s *Server) clientEncoding(r *http.Request) string {
	return clientContentEncoding(r, s.zstdPieces)
}

// clientAcceptsGzip reports whether this request may receive a pre-encoded
// gzip body. It defers to httpcompat.Encoding, the same selector front.Deflate
// uses for its own negotiation, so a pre-encoded body is only sent when the
// middleware would itself have chosen gzip (front.Deflate passes a pre-set
// Content-Encoding through untouched, including its 406 policy). When front is
// not in the chain, at worst the response goes out as identity bytes and the
// caller compresses them.
func clientAcceptsGzip(r *http.Request) bool {
	return httpcompat.Encoding(r.Header.Get("Accept-Encoding")) == "gzip"
}

// clientContentEncoding selects the wire encoding for a pre-encoded recorded
// response: "gzip", "zstd" or "identity", or "" when the client accepts
// nothing (front.Deflate answers 406 on the identity path). zstd is the
// ENGINE-50 addition: a zstd member is served only to clients that accept it
// and prefer it over gzip, so no existing client changes encoding; every
// other decision is left to the httpcompat selector, byte for byte. The scan
// is allocation-free (in-place token walk over a stack state).
func clientContentEncoding(r *http.Request, zstdEnabled bool) string {
	header := r.Header.Get("Accept-Encoding")
	if !zstdEnabled {
		return httpcompat.Encoding(header)
	}
	// Candidate state: quality, rejection, and whether the name was
	// mentioned at all. Preference order on equal quality follows the
	// httpcompat convention (gzip over identity); zstd sits between them.
	var q = [3]float64{-1, -1, -1} // gzip, zstd, identity
	rejected := [3]bool{}
	mentioned := [3]bool{}
	expanded := [3]bool{}
	wildcardQ := -1.0

	parse := func(part string) {
		name, params, _ := strings.Cut(part, ";")
		name = strings.TrimSpace(name)
		params = strings.TrimSpace(params)
		quality := 1.0
		if strings.HasPrefix(params, "q=") {
			quality = parseQuality(params[2:])
		}
		index := -1
		switch {
		case foldEqASCII(name, "gzip"):
			index = 0
		case foldEqASCII(name, "zstd"):
			index = 1
		case foldEqASCII(name, "identity"):
			index = 2
		}
		if index >= 0 {
			mentioned[index] = true
			if quality == 0 {
				rejected[index] = true
			} else if quality > q[index] {
				q[index] = quality
			}
			return
		}
		if name == "*" {
			wildcardQ = quality
		}
	}
	for header != "" {
		var part string
		if comma := strings.IndexByte(header, ','); comma >= 0 {
			part, header = header[:comma], header[comma+1:]
		} else {
			part, header = header, ""
		}
		part = strings.TrimSpace(part)
		if part != "" {
			parse(part)
		}
	}
	// Wildcard expansion: the unmentioned names at the wildcard's quality.
	// An expansion counts as "mentioned" for the identity-fallback rule even
	// at q=0, exactly as httpcompat treats a wildcard-expanded identity.
	if wildcardQ >= 0 {
		if wildcardQ == 0 {
			for i := 0; i < 3; i++ {
				if !mentioned[i] {
					rejected[i] = true
					expanded[i] = true
				}
			}
		} else {
			for i := 0; i < 3; i++ {
				if !mentioned[i] && q[i] < 0 {
					q[i] = wildcardQ
					expanded[i] = true
				}
			}
		}
	}
	// The middleware appended an identity fallback whenever the header never
	// mentioned identity; that fallback is never rejected.
	identityFallback := !mentioned[2] && !expanded[2]

	// The identity fallback matches httpcompat exactly: it never competes
	// with a real candidate; it is the answer only when nothing the client
	// named is acceptable.
	best := -1
	bestQ := -1.0
	for i := 0; i < 3; i++ {
		if rejected[i] || q[i] < 0 {
			continue
		}
		if q[i] > bestQ || (q[i] == bestQ && i < best) {
			best, bestQ = i, q[i]
		}
	}
	if best < 0 {
		if identityFallback {
			return "identity"
		}
		return ""
	}
	switch best {
	case 0:
		return "gzip"
	case 1:
		return "zstd"
	default:
		return "identity"
	}
}

// foldEqASCII compares two ASCII strings case-insensitively without
// allocation (Accept-Encoding tokens are ASCII; the fold mirrors the
// canonicalisation the selector historically applied via strings.ToLower).
func foldEqASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// parseQuality parses the numeric prefix of a q= parameter, mirroring
// httpcompat.quality: a malformed value yields 0 (the failed ParseFloat
// result was assigned unconditionally).
func parseQuality(value string) float64 {
	end := 0
	for end < len(value) && (value[end] >= '0' && value[end] <= '9' || value[end] == '.') {
		end++
	}
	if end == 0 {
		return 1.0
	}
	q, _ := strconv.ParseFloat(value[:end], 64)
	return q
}

// recordedCompressor is one pooled compressor state: a gzip writer plus the
// zstd writer sharing one output buffer. The buffer is capped like the
// response buffer pool, so one miss-compressed page cannot pin an unbounded
// buffer per P. One pool per gzip level (compress/gzip has no level change
// method that outlives Reset in this Go version), so a level change keeps a
// writer per level instead of rebuilding one per fill.
type recordedCompressor struct {
	writer     *gzip.Writer
	buf        bytes.Buffer
	zstdWriter *zstd.Writer
}

var recordedCompressorPools [10]sync.Pool

func init() {
	for level := 1; level <= 9; level++ {
		level := level
		recordedCompressorPools[level].New = func() any {
			writer, _ := gzip.NewWriterLevel(nil, level)
			return &recordedCompressor{writer: writer}
		}
	}
}

func compressorPool(level int) *sync.Pool {
	if level < 1 || level > 9 {
		level = 6 // guarded levels only reach here through misuse
	}
	return &recordedCompressorPools[level]
}

// recordedGzipFillLevel is the gzip level used when cached members are
// compressed at fill (ENGINE-50): level 9 by default; CAMPFIRE_RECORDED_GZIP_LEVEL
// configures it, and 6 is the pre-engine value kept for the A/B and rollback
// switch. Compression is a one-time fill cost, so the higher level's CPU is
// never on the request path; smaller members mean fewer socket-write bytes on
// every gzip request.
var recordedGzipFillLevel = 9

func setRecordedGzipFillLevel(level int) { recordedGzipFillLevel = level }

// compressGzip returns a complete gzip member of raw at the configured fill
// level. It is used when a piece is stored; the writer and buffer are reset
// before use and the returned bytes are an independent copy, so a pooled
// buffer can never leak one caller's bytes into another's response.
func compressGzip(raw []byte) []byte { return compressGzipLevel(raw, recordedGzipFillLevel) }

// compressGzipLevel is compressGzip at an explicit level (benchmarks and the
// legacy level-6 A/B).
func compressGzipLevel(raw []byte, level int) []byte { return compressGzipInto(nil, raw, level) }

// compressGzipInto appends a gzip member of raw to dst at level, reusing the
// pooled writer of that level. dst may alias the caller's own buffer; the
// member is fully rewritten from index len(dst) onwards.
func compressGzipInto(dst, raw []byte, level int) []byte {
	c := compressorPool(level).Get().(*recordedCompressor)
	c.buf.Reset()
	c.writer.Reset(&c.buf)
	c.writer.Header.OS = 3
	_, _ = c.writer.Write(raw)
	_ = c.writer.Close()
	c.writer.Reset(nil)
	dst = append(dst, c.buf.Bytes()...)
	if c.buf.Cap() <= 1<<20 {
		compressorPool(level).Put(c)
	}
	return dst
}

// recordedZstdFillLevel is the zstd fill level (ENGINE-50): cache-fill
// compression is a one-time cost, so members are compressed at libzstd's best
// level the way the gzip fill uses level 9. Decode cost does not depend on
// the level, so the larger compression ratio is pure per-request byte savings.
const recordedZstdFillLevel = 19

// compressZstd returns a complete zstd frame of raw at the fill level, for
// pieces stored under the zstd member variant. Like compressGzip it is only
// paid at fill time.
func compressZstd(raw []byte) []byte { return compressZstdInto(nil, raw) }

// compressZstdInto appends a complete zstd frame of raw to dst. The writer
// and buffer are reset before use and the returned bytes are an independent
// copy (the buffer is pooled; the caller's dst slice owns the appended bytes).
func compressZstdInto(dst, raw []byte) []byte {
	c := compressorPool(9).Get().(*recordedCompressor)
	if c.zstdWriter == nil {
		c.zstdWriter, _ = zstd.NewWriterLevel(nil, recordedZstdFillLevel)
	}
	c.buf.Reset()
	_ = c.zstdWriter.Reset(&c.buf)
	_, _ = c.zstdWriter.Write(raw)
	_ = c.zstdWriter.End()
	dst = append(dst, c.buf.Bytes()...)
	if c.buf.Cap() <= 1<<20 {
		compressorPool(9).Put(c)
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

// loadedAtScratch caches one (timestamp value -> members) conversion per
// pooled scratch: the room page's loadedAt changes at most once per
// millisecond, so a burst of requests reuses the members. Comparing the value
// makes a stale pooled member impossible. raw is the value's bytes (the
// identity path's part), member is the gzip member, zstd the zstd frame when
// enabled. Digest is deliberately zero: the loadedAt slot is excluded from the
// response ETag, and only the assembled bytes ever read the piece.
type loadedAtScratch struct {
	value  string
	raw    []byte
	member []byte
	zstd   []byte
	entry  piececache.Entry
}

var loadedAtScratches = sync.Pool{New: func() any { return &loadedAtScratch{} }}

// borrowLoadedAt returns a dynamic response piece scratch for value. The
// caller must release it once the assembled response no longer reads the
// entry; the method call avoids a per-request closure allocation. wantZstd
// asks for the zstd frame because this request will assemble one; building
// the frame is a per-millisecond fill cost, so it is only paid when the pool
// will hand the member out.
func borrowLoadedAt(value string, wantZstd bool) *loadedAtScratch {
	scratch := loadedAtScratches.Get().(*loadedAtScratch)
	if scratch.member == nil || scratch.value != value {
		scratch.raw = append(scratch.raw[:0], value...)
		scratch.member = compressGzipInto(scratch.member[:0], scratch.raw, recordedGzipFillLevel)
		scratch.zstd = scratch.zstd[:0]
		scratch.value = value
	}
	if wantZstd && len(scratch.zstd) == 0 && len(scratch.raw) > 0 {
		scratch.zstd = compressZstdInto(scratch.zstd[:0], scratch.raw)
	}
	scratch.entry = piececache.Entry{Raw: scratch.raw, Member: scratch.member, Zstd: scratch.zstd}
	return scratch
}

func (c *loadedAtScratch) release() { loadedAtScratches.Put(c) }
