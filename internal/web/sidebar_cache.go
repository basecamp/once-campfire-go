package web

import "strconv"

// sidebarFragmentKey returns the fragment-cache key for one user's sidebar at
// one sidebar version (ENGINE-20). The key is derived from the version
// registry alone — the user id plus the database-sidebar version that every
// sidebar-visible write bumps (internal/database/versions.go) — so a cache
// hit needs no room, membership or placeholder reads. The previous content
// hash key (sidebar_cache_key over every rendered field) is gone: it forced
// the full sidebar read before the cache could be consulted, which was the
// measured 40.8 % of sidebar CPU. Version values are opaque; the key format
// itself is not part of any contract, only its one-to-one map from
// (user, version) to entry.
func sidebarFragmentKey(userID int64, version uint64) string {
	b := make([]byte, 0, 24)
	b = append(b, "sidebar/"...)
	b = strconv.AppendUint(b, version, 10)
	b = append(b, '/')
	b = strconv.AppendInt(b, userID, 10)
	return string(b)
}
