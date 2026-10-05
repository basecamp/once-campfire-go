package web

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"html/template"

	"net/http"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// The marker exists only during template execution. The actual response inserts
// the cached message list without copying it through template/fmt/page buffers.

func (s *Server) messageList(ctx context.Context, messages []database.Message) (fragmentEntry, error) {
	var key strings.Builder
	key.Grow(13 + len(messages)*40)
	key.WriteString("message-list/")
	var number [32]byte
	for _, message := range messages {
		key.WriteString("message/")
		key.Write(strconv.AppendInt(number[:0], message.UpdatedAt.UnixMicro(), 10))
		key.WriteByte('/')
		key.Write(strconv.AppendInt(number[:0], message.ID, 10))
		key.WriteByte('/')
	}
	if entry, ok := s.fragments.entry(key.String()); ok {
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
	etag, modified := messageValidators(messages, false)
	frameETag, _ := messageValidators(messages, true)
	entry := s.fragments.putEntry(fragmentEntry{key: key.String(), html: html, messageETag: etag, frameETag: frameETag, modified: modified})
	if entry.payload == nil {
		entry.payload = []byte(html)
		entry.digest = sha256.Sum256(entry.payload)
	}
	return entry, nil
}

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
	digests := [][32]byte{sha256.Sum256(parts[0]), fragment.digest, sha256.Sum256(parts[2])}
	writeParts(w, status, parts, digests)
}

func writePageShell(w http.ResponseWriter, status int, shell fragmentEntry, loadedAt string, fragment fragmentEntry) {
	parts := make([][]byte, 0, len(shell.shell))
	digests := make([][32]byte, 0, len(shell.shell))
	stamp := []byte(loadedAt)
	stampDigest := sha256.Sum256(stamp)
	for _, part := range shell.shell {
		payload, digest := part.payload, part.digest
		switch part.slot {
		case 1:
			payload, digest = fragment.payload, fragment.digest
			if payload == nil {
				payload = []byte(fragment.html)
			}
		case 2:
			payload, digest = stamp, stampDigest
		}
		parts = append(parts, payload)
		digests = append(digests, digest)
	}
	writeParts(w, status, parts, digests)
}

func writeParts(w http.ResponseWriter, status int, parts [][]byte, digests [][32]byte) {
	if w.Header().Get("ETag") == "" {
		// Like Rust's Body::Parts, digest boundaries and cached fragment hashes.
		hash := sha256.New()
		for i, part := range parts {
			var size [8]byte
			binary.LittleEndian.PutUint64(size[:], uint64(len(part)))
			hash.Write(size[:])
			hash.Write(digests[i][:])
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
