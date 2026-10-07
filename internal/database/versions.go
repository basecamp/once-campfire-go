package database

import (
	"context"
	"hash/fnv"
	"strconv"
	"sync"
	"sync/atomic"
)

// SidebarVersion source choice (ENGINE-20): an in-process version registry
// seeded from the database on first use and bumped by every sidebar-visible
// write, rather than a per-request SQL fingerprint. The registry is the only
// option that is provably complete: the version returned by SidebarVersion
// changes exactly when an audited write helper runs (each bump happens after
// the helper's transaction commits), and the audit enumeration in
// internal/database/versions_test.go asserts that every write helper whose
// rows feed the sidebar template bumps. A finite SQL fingerprint (counts,
// maxes, sums) cannot be proven injective over arbitrary states, and re-reading
// the rows defeats the purpose.
//
// The seed captures the sidebar-relevant state (the counts and latest
// updated_at of the four tables the sidebar template reads) at first use, so a
// database replaced by an external process before the first sidebar request
// yields a distinct initial version. Writes by another process after first use
// are picked up on restart, not live — the same documented single-process
// limit as the design's room/membership registries and the reference's own
// in-process fragment caches (plans/2026-10-05-engine-design.md §3.5).
//
// Bumps are deliberately coarse: one counter for every user's sidebar. A
// global counter is complete by construction (any sidebar-visible write
// changes every key's version) at the cost of unrelated users' fragments
// re-rendering on the next request; the fragment cache's LRU bounds that
// churn. Version values are opaque; only equality matters.
type sidebarVersions struct {
	seeded atomic.Bool // guards the seed below; publish order makes the seed visible
	mu     sync.Mutex  // serializes the one-time seed query
	seed   uint64
	bumps  atomic.Uint64
}

// SidebarVersion returns the current sidebar fragment version: a fingerprint
// of the sidebar-relevant rows at first use plus one per successful
// sidebar-visible write since. The call seeds the registry from the database
// on first use (one query for the process); later calls are atomic loads and
// allocate nothing.
func (d *DB) SidebarVersion(ctx context.Context) (uint64, error) {
	if !d.sidebar.seeded.Load() {
		if err := d.seedSidebarVersion(ctx); err != nil {
			return 0, err
		}
	}
	return d.sidebar.seed + d.sidebar.bumps.Load(), nil
}

// seedSidebarVersion fingerprints the sidebar-relevant tables exactly once.
func (d *DB) seedSidebarVersion(ctx context.Context) error {
	d.sidebar.mu.Lock()
	defer d.sidebar.mu.Unlock()
	if d.sidebar.seeded.Load() {
		return nil
	}
	const query = `SELECT
		(SELECT count(*) FROM rooms) + (SELECT count(*) FROM memberships)
		+ (SELECT count(*) FROM users) + (SELECT count(*) FROM accounts),
		coalesce((SELECT max(updated_at) FROM rooms),'') || '/' ||
		coalesce((SELECT max(updated_at) FROM memberships),'') || '/' ||
		coalesce((SELECT max(updated_at) FROM users),'') || '/' ||
		coalesce((SELECT max(updated_at) FROM accounts),'')`
	var count int64
	var latest string
	if err := d.Read.QueryRowContext(ctx, query).Scan(&count, &latest); err != nil {
		return err
	}
	h := fnv.New64a()
	h.Write([]byte(strconv.FormatInt(count, 10)))
	h.Write([]byte{'/'})
	h.Write([]byte(latest))
	d.sidebar.seed = h.Sum64()
	d.sidebar.seeded.Store(true)
	return nil
}

// bumpSidebarVersion records one sidebar-visible write. It is called by every
// audited write helper after its transaction commits; the audit test
// (versions_test.go) fails if a helper that changes rows the sidebar template
// reads does not call it.
func (d *DB) bumpSidebarVersion() {
	d.sidebar.bumps.Add(1)
}
