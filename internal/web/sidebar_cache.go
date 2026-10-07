package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"html/template"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// The sidebar split (upstream main PR #9, adopted): sidebar.html is a shell —
// layout-start, the pre-rendered frame, layout-end — and "sidebar-frame" is
// the cacheable part. The layout (account, flash, user profile) is rendered
// fresh on every document request; the frame is cached.
//
// Two independent keys serve it (ENGINE-20 + PR #9):
//
//   - sidebarFragmentKey: the version-registry key of the whole-response
//     fragment cache this design replaced. It still names what version a
//     response was served for, but the served bytes are the frame alone and
//     the gate (below) records them.
//   - sidebarCacheKey (the content key): every value the frame reads. The
//     frame fragment is stored under it, so account-only writes — which bump
//     the registry (versions.go) but no frame input — keep the entry: a
//     request after such a write re-renders the frame deterministically and
//     stores it under the same key, keeping the fragment cache's entry count
//     stable for layout-only changes.
//
// Server.sidebar is the gate: (user -> registry version + frame content key).
// A warm request serves the frame through the fresh layout without reading
// rooms, memberships or placeholders at all; a request after any sidebar-
// visible write misses the gate and re-reads. The account read cache
// (read_cache.go) keeps the layout's own reads off the database on warm
// requests, keyed by the same registry version.

// sidebarFragmentKey returns the fragment-cache key for one user's sidebar at
// one sidebar version (ENGINE-20). The key is derived from the version
// registry alone — the user id plus the database-sidebar version that every
// sidebar-visible write bumps (internal/database/versions.go) — so a gate
// check needs no room, membership or placeholder reads. Version values are
// opaque; the key format itself is not part of any contract, only its
// one-to-one map from (user, version) to entry.
func sidebarFragmentKey(userID int64, version uint64) string {
	b := make([]byte, 0, 24)
	b = append(b, "sidebar/"...)
	b = strconv.AppendUint(b, version, 10)
	b = append(b, '/')
	b = strconv.AppendInt(b, userID, 10)
	return string(b)
}

// servedGate records what a user's last sidebar miss rendered: the registry
// version it was served for and the content key the frame fragment is stored
// under.
type servedGate struct {
	version  uint64
	frameKey string
}

// sidebarGate loads the user's served gate.
func (s *Server) sidebarGate(user int64) (servedGate, bool) {
	value, ok := s.sidebarGates.Load(user)
	if !ok {
		return servedGate{}, false
	}
	return value.(servedGate), true
}

// sidebarHTML renders the sidebar frame and caches it under its content key;
// the layout around it is rendered fresh per request. The served gate is
// recorded (only when the handler passed a registry version), so the next
// request with the same version serves the cached frame before any sidebar
// read. A frame already cached under the same content key (an account-only
// write bumped the registry and the miss re-rendered deterministically) is
// returned without a second render, keeping the entry count stable.
func (s *Server) sidebarHTML(p page) (template.HTML, error) {
	key := sidebarCacheKey(p)
	if fragment, ok := s.fragments.get(key); ok {
		s.recordSidebarGate(p, key)
		return fragment, nil
	}
	var b bytes.Buffer
	if err := s.templates.ExecuteTemplate(&b, "sidebar-frame", p); err != nil {
		return "", err
	}
	fragment := template.HTML(b.String())
	s.fragments.put(key, fragment)
	s.recordSidebarGate(p, key)
	return fragment, nil
}

func (s *Server) recordSidebarGate(p page, frameKey string) {
	if p.gateVersion == 0 {
		return
	}
	s.sidebarGates.Store(p.User.ID, servedGate{version: p.gateVersion, frameKey: frameKey})
}

// sidebarCacheKey keys every value the sidebar frame reads. Authorization
// and membership data are still read afresh before looking up the rendered
// fragment; the account row and other layout inputs are deliberately absent,
// so layout-only changes neither invalidate nor re-key the frame.
func sidebarCacheKey(p page) string {
	var key strings.Builder
	user := func(u database.User) {
		fmt.Fprintf(&key, "u%d/%d/%d:%s/", u.ID, u.UpdatedAt.UnixMicro(), len(u.Name), u.Name)
	}
	user(p.User)
	fmt.Fprintf(&key, "%t/%s/%s/", p.CanCreateRooms, p.RoomsStream, p.UserRoomsStream)
	for _, room := range p.SidebarRooms {
		fmt.Fprintf(
			&key,
			"r%d/%d/%t/%d:%s/%d:%s/",
			room.ID,
			room.UpdatedAt.UnixMicro(),
			room.Unread,
			len(room.Type),
			room.Type,
			len(room.Name),
			room.Name,
		)
		for _, member := range room.Members {
			user(member)
		}
		key.WriteByte(';')
	}
	key.WriteByte('|')
	for _, member := range p.Placeholders {
		user(member)
	}
	return fmt.Sprintf("sidebar/%x", sha256.Sum256([]byte(key.String())))
}
