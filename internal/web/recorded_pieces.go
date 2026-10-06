package web

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/piececache"
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

// recordedShellKey identifies a shell by every page input the template reads,
// minus the message list (the payload piece), its marker and the loadedAt value
// (inserted per request). The message count is part of the key because
// layout-start renders len(.Messages) on the search page.
func recordedShellKey(name string, p page) (string, error) {
	messageCount := len(p.Messages)
	p.Messages, p.MessagesHTML, p.LoadedAt = nil, "", ""
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("recorded-shell/%s/%x/%d", name, sha256.Sum256(raw), messageCount), nil
}

func recordedShellLayoutKey(key string) string { return key + "/layout" }
func recordedShellSegmentKey(key string, i int) string {
	return key + "/" + strconv.Itoa(i)
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
// never a stale one.
func (s *Server) shellPieces(p page, name string) (recordedShell, error) {
	key, err := recordedShellKey(name, p)
	if err != nil {
		return recordedShell{}, err
	}
	if shell, ok := s.loadShell(key); ok {
		return shell, nil
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
		return recordedShell{}, err
	}
	var shell recordedShell
	shell.count = layout.count
	for i := 0; i < layout.count-1; i++ {
		if layout.slots[i] == slotLoadedAt {
			shell.loadedMask |= 1 << i
		}
	}
	s.pieces.Put(recordedShellLayoutKey(key), []byte{byte(layout.count), shell.loadedMask}, nil)
	for i := 0; i < layout.count; i++ {
		entry, _ := s.pieces.Put(recordedShellSegmentKey(key, i), layout.segments[i], compressGzip(layout.segments[i]))
		shell.segments[i] = entry
	}
	return shell, nil
}

// loadShell rebuilds a shell from the cache. A missing layout or segment is a
// miss: the caller renders and re-stores.
func (s *Server) loadShell(key string) (recordedShell, bool) {
	layout := s.pieces.Get(recordedShellLayoutKey(key))
	if layout == nil || len(layout.Raw) != 2 {
		return recordedShell{}, false
	}
	count, mask := int(layout.Raw[0]), layout.Raw[1]
	if count < 2 || count > 3 || mask>>(count-1) != 0 {
		return recordedShell{}, false
	}
	shell := recordedShell{count: count, loadedMask: mask}
	for i := 0; i < count; i++ {
		entry := s.pieces.Get(recordedShellSegmentKey(key, i))
		if entry == nil {
			return recordedShell{}, false
		}
		shell.segments[i] = entry
	}
	return shell, true
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
// form and copies nothing.
func (s *Server) writeRecordedPieces(w http.ResponseWriter, r *http.Request, status int, name string, p page, recorded recordedPayload) (bool, error) {
	payload := recorded.piece
	if payload == nil {
		return false, nil
	}
	shell, err := s.shellPieces(p, name)
	if err != nil {
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
	if clientAcceptsGzip(r) {
		var loaded *piececache.Entry
		if shell.loadedMask != 0 {
			raw := []byte(p.LoadedAt)
			loaded = piececache.NewEntry(raw, compressGzip(raw))
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
		var dst []byte
		if buffered != nil {
			dst = buffered.encoded[:0]
		}
		encoded, _, err := piececache.Assemble(dst, piececache.Gzip, parts...)
		if err != nil {
			return true, err
		}
		s.recordedAssemblies.Add(1)
		h.Set("Content-Encoding", "gzip")
		addVaryAcceptEncoding(h)
		w.WriteHeader(status)
		if sw, ok := w.(*sessionWriter); ok && sw.failed {
			return true, nil
		}
		if buffered == nil {
			_, err = w.Write(encoded)
			return true, err
		}
		buffered.encoded = encoded
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
// wildcard) is already present.
func addVaryAcceptEncoding(h http.Header) {
	for _, value := range strings.Split(h.Get("Vary"), ",") {
		if strings.EqualFold(strings.TrimSpace(value), "Accept-Encoding") || strings.TrimSpace(value) == "*" {
			return
		}
	}
	if old := h.Get("Vary"); old != "" {
		h.Set("Vary", old+",Accept-Encoding")
	} else {
		h.Set("Vary", "Accept-Encoding")
	}
}

// clientAcceptsGzip reports whether this request may receive a pre-encoded gzip
// body. It is a conservative subset of front.encoding's selection: pre-encoding
// is allowed only when gzip is explicitly acceptable at full weight and no
// identity token with a positive q appears. A false negative only means the
// response goes out as identity bytes and front.Deflate compresses it; a false
// positive would let a pre-encoded body through front.Deflate's
// Content-Encoding pass-through for a client that declined gzip.
func clientAcceptsGzip(r *http.Request) bool {
	header := r.Header.Get("Accept-Encoding")
	if header == "" {
		return false
	}
	gzip := false
	for tokens := 0; tokens < 16; tokens++ {
		part, rest, _ := strings.Cut(header, ",")
		header = rest
		name, params, _ := strings.Cut(part, ";")
		name = strings.ToLower(strings.TrimSpace(name))
		q := 1.0
		params = strings.TrimSpace(params)
		if strings.HasPrefix(params, "q=") {
			value := strings.TrimPrefix(params, "q=")
			end := 0
			for end < len(value) && (value[end] >= '0' && value[end] <= '9' || value[end] == '.') {
				end++
			}
			if end > 0 {
				q, _ = strconv.ParseFloat(value[:end], 64)
			}
		}
		switch name {
		case "gzip":
			gzip = gzip || q >= 1
		case "identity":
			if q > 0 {
				return false
			}
		}
		if header == "" {
			break
		}
	}
	return gzip
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
// is stored and for the per-request loadedAt piece; the writer and buffer are
// reset before use and the returned bytes are an independent copy, so a pooled
// buffer can never leak one caller's bytes into another's response.
func compressGzip(raw []byte) []byte {
	c := recordedCompressors.Get().(*recordedCompressor)
	c.buf.Reset()
	c.writer.Reset(&c.buf)
	c.writer.Header.OS = 3
	_, _ = c.writer.Write(raw)
	_ = c.writer.Close()
	c.writer.Reset(nil)
	out := append([]byte(nil), c.buf.Bytes()...)
	if c.buf.Cap() <= 1<<20 {
		recordedCompressors.Put(c)
	}
	return out
}
