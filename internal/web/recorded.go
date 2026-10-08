package web

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"html/template"
	"strconv"

	"net/http"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/piececache"
)

// The marker exists only during template execution. The actual response inserts
// the cached message list without copying it through template/fmt/page buffers.

// recordedPayload is a rendered message list in whichever representation the
// active path uses: one immutable piece (raw+deflate fragment+zstd frame+
// digest) when CAMPFIRE_RECORDED_PIECES is on, or the legacy HTML fragment
// when it is off.
type recordedPayload struct {
	piece    *piececache.Entry
	fragment fragmentEntry
}

func messageListKey(messages []database.Message) string {
	var key strings.Builder
	key.WriteString("message-list/")
	for _, message := range messages {
		key.WriteString(messageCacheKey(message))
		key.WriteByte('/')
	}
	return key.String()
}

// messageListIdentity hashes the message identities that decide the list's
// bytes — every message's id and updated-at stamp — together with the
// observed database generation (ef00d84): the piece namespace moves with
// every commit, so an in-flight render at generation N can never populate
// generation N+1, and external-process commits invalidate by generation
// alone. It appends into a stack scratch and hashes in place, so a
// 40-message page costs no per-message Stamp string or FormatInt allocation.
// The stamp is the Unix microsecond value, the precision the database stores
// (database.Stamp writes exactly microseconds), so an edit still changes the
// identity. cacheable is false when the generation has not been observed yet
// (a fresh connection reports PRAGMA data_version 0): like fragmentKey's
// "uncached/" policy, nothing may be retained from a render the observer has
// not synced.
func (s *Server) messageListIdentity(ctx context.Context, messages []database.Message) (identity [32]byte, cacheable bool) {
	var scratch [2048]byte
	buf := scratch[:0]
	version := uint64(0)
	if info := requestMetadata(ctx); info != nil {
		version = info.databaseVersion
	} else {
		// Background helpers have no HTTP entry metadata; read the
		// generation directly, exactly like fragmentKey's fallback.
		version, _ = s.DB.ResponseVersion(ctx)
	}
	if version == 0 {
		return identity, false
	}
	buf = strconv.AppendUint(buf, version, 10)
	buf = append(buf, ';')
	for i := range messages {
		buf = strconv.AppendInt(buf, messages[i].ID, 10)
		buf = append(buf, ';')
		buf = strconv.AppendInt(buf, messages[i].UpdatedAt.UnixMicro(), 10)
		buf = append(buf, '|')
	}
	return sha256.Sum256(buf), true
}

// recordedMessageList returns the message-list payload for a recorded page,
// caching it as a compressed piece on the piece path. The key is content
// derived (message ids and updated-at stamps) plus the observed generation,
// so writes invalidate by key change alone. needGzip asks for the gzip member
// because this request will assemble a gzip body; needZstd the zstd frame
// (ENGINE-50). When the cache cannot store anything and no compressed body is
// being assembled, the compression would be discarded immediately, so it is
// skipped.
func (s *Server) recordedMessageList(ctx context.Context, messages []database.Message, needGzip, needZstd bool) (recordedPayload, error) {
	if !s.recordedPieces {
		entry, err := s.messageList(ctx, messages)
		return recordedPayload{fragment: entry}, err
	}
	identity, cacheable := s.messageListIdentity(ctx, messages)
	if cacheable {
		if entry := s.pieces.GetDigest(identity); entry != nil {
			return recordedPayload{piece: entry}, nil
		}
	}
	views, err := s.messageItems(ctx, messages)
	if err != nil {
		return recordedPayload{}, err
	}
	var body strings.Builder
	for _, view := range views {
		body.WriteString(string(view.Fragment))
	}
	raw := []byte(body.String())
	var fragment, zstdMember []byte
	if needGzip || s.pieces.Enabled() {
		fragment = compressFragment(raw)
	}
	if s.zstdPieces && (needZstd || s.pieces.Enabled()) {
		zstdMember = compressZstd(raw)
	}
	if !cacheable {
		// Generation not observed yet: never retain (the fragment namespace
		// under fragmentKey is likewise inactive), but the response still
		// needs the immutable piece.
		return recordedPayload{piece: piececache.NewEntry(raw, fragment, zstdMember)}, nil
	}
	entry, _ := s.pieces.PutDigestZstd(identity, raw, fragment, zstdMember)
	return recordedPayload{piece: entry}, nil
}

func (s *Server) messageList(ctx context.Context, messages []database.Message) (fragmentEntry, error) {
	key := s.fragmentKey(ctx, messageListKey(messages))
	if entry, ok := s.fragments.entry(key); ok {
		return entry, nil
	}
	views, err := s.messageItems(ctx, messages)
	if err != nil {
		return fragmentEntry{}, err
	}
	var body strings.Builder
	for _, view := range views {
		body.WriteString(string(view.Fragment))
	}
	html := template.HTML(body.String())
	s.fragments.put(key, html)
	if entry, ok := s.fragments.entry(key); ok {
		return entry, nil
	}
	return fragmentEntry{html: html, digest: sha256.Sum256([]byte(html))}, nil
}

// writeRecorded splits the rendered page at the message marker and hands the
// cached fragment to the response buffer, deriving a weak validator from the
// digest boundaries exactly like the reference's Body::Parts.
func writeRecorded(w http.ResponseWriter, status int, rendered, marker string, fragment fragmentEntry) {
	before, after, found := strings.Cut(rendered, marker)
	if !found {
		http.Error(w, "Missing message insertion point", 500)
		return
	}
	payload := fragment.payload
	if payload == nil {
		payload = []byte(fragment.html)
	}
	parts := [][]byte{[]byte(before), payload, []byte(after)}
	if w.Header().Get("ETag") == "" {
		// Like Rust's Body::Parts, digest boundaries and cached fragment hashes.
		hash := sha256.New()
		for i, part := range parts {
			var size [8]byte
			binary.LittleEndian.PutUint64(size[:], uint64(len(part)))
			hash.Write(size[:])
			digest := fragment.digest
			if i != 1 {
				digest = sha256.Sum256(part)
			}
			hash.Write(digest[:])
		}
		w.Header().Set("ETag", fmt.Sprintf("W/\"%x\"", hash.Sum(nil)[:16]))
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "max-age=0, private, must-revalidate")
	}
	w.WriteHeader(status)
	if sw, ok := w.(*sessionWriter); ok && sw.failed {
		return
	}
	target := w
	for {
		if buffered, ok := target.(*responseBuffer); ok {
			buffered.parts = parts
			return
		}
		if wrapper, ok := target.(interface{ Unwrap() http.ResponseWriter }); ok {
			target = wrapper.Unwrap()
		} else {
			break
		}
	}
	for _, part := range parts {
		w.Write(part)
	}
}
