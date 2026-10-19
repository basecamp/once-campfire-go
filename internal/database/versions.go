package database

import (
	"context"
	"hash/fnv"
	"strconv"
	"sync"
	"sync/atomic"
)

// This file holds the in-process version counters behind two caches, each
// with its own invalidation contract:
//
//   - SidebarVersion (ENGINE-20) is the sidebar fragment version: an in-process
//     registry seeded from the database on first use and bumped by every
//     sidebar-visible write. The sidebars cache keys its fragments on it.
//   - CorpusVersion and MembershipVersion (ENGINE-30) are the search result
//     cache versions: global counters bumped by FTS-affecting and
//     membership-affecting writes respectively, keying cached search pages
//     (user, normalized query, corpusVersion, membershipVersion).
//   - SessionVersion and UserVersion (ENGINE-40b) are the cable publication
//     authorization generations: sessions-table writes and users.status writes
//     respectively. Together with MembershipVersion they key the hub's
//     versioned authorization cache ((room, token) -> authorized user), so a
//     publication never serves an authorization result that outlived a
//     session deletion, a ban or a membership revocation.
//
// All counters are bumped only after the write transaction commits, and only
// on success: a reader can never observe a new version with old data. Version
// values are opaque; only equality matters.

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

// Version counters for the web search result cache (ENGINE-30). The cache is
// keyed by (user, normalized query, corpusVersion, membershipVersion); a
// bumped counter makes every cached search page that depended on the changed
// data miss and re-read.
//
// corpusVersion counts every FTS-affecting write: message create, edit and
// delete, boost change, and the room destroy that drops a room's index rows.
// It is a single global counter — deliberately conservative: a message
// anywhere in the account invalidates every cached query, never a stale one.
//
// membershipVersion counts membership-affecting writes: room creation,
// membership grants and revocations, involvement changes and the account
// lifecycle writes that grant or revoke memberships. The search query joins
// memberships for its result scoping, and membership is the only per-user
// input the cached page still depends on (the search sidebar renders recent
// searches, not the room list).
//
// Both counters are bumped only after the write transaction commits, and only
// on success: a reader can therefore never observe a new version with old
// data. A bump for a change that committed before the version snapshot still
// leaves the entry tagged with the older version, which is never asked for
// again once the counter moves — stale entries are unreachable, not served.
// Rolled-back transactions bump nothing. The web layer additionally re-checks
// the versions before storing, so a write that commits between its reads and
// its store cannot tag fresh rows with an old version either.
func (d *DB) CorpusVersion() int64     { return d.corpusVersion.Load() }
func (d *DB) MembershipVersion() int64 { return d.membershipVersion.Load() }

// Publication authorization generations (ENGINE-40b). The cable hub keys its
// authorization cache ((room, token) -> authorized user) on the triple
// (SessionVersion, MembershipVersion, UserVersion), and each entry stores the
// triple it was computed under: a lookup serves an entry only while all three
// counters still match, so no authorization result outlives its generations.
//
//   - SessionVersion counts sessions-table writes that insert or delete rows
//     (login, logout, ban, deactivation), never updates of non-authorization
//     columns (RefreshSession only refreshes last_active_at and friends).
//   - UserVersion counts users.status writes: ban, unban and deactivation.
//   - MembershipVersion (ENGINE-30) counts membership grant/revocation writes,
//     which are also the room-visibility writes (open/closed conversion and
//     room destruction rewrite the membership set).
//
// The cache is keyed per (room, token), so inserting a new session row can
// only add results for a token that was never cached; the bumps exist to keep
// the audit contract "every authorization-relevant write moves a generation"
// simple and complete, and their cost is one re-warm of the hub's cache on a
// cold path (a login or an admin write), never the publish path.
//
// Like the other counters, every bump runs after the write transaction
// commits and only on success, and the same caveat applies: the invalidation
// covers the audited write helpers in this package. Authorization state
// changed by out-of-band SQL (other processes, tests writing rows directly)
// is not visible to the cable layer until a generation moves — the documented
// single-process limit of the other versioned caches.
func (d *DB) SessionVersion() int64 { return d.sessionVersion.Load() }
func (d *DB) UserVersion() int64    { return d.userVersion.Load() }

// bumpSessionVersion records one sessions-table insert/delete.
func (d *DB) bumpSessionVersion() { d.sessionVersion.Add(1) }

// bumpUserVersion records one users.status write.
func (d *DB) bumpUserVersion() { d.userVersion.Add(1) }
