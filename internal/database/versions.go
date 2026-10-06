package database

// Version counters for the web search result cache (ENGINE-30). The cache is
// keyed by (user, normalized query, corpusVersion, membershipVersion); a
// bumped counter makes every cached search page that depended on the changed
// data miss and re-read.
//
// corpusVersion counts every FTS-affecting write: message create, edit and
// delete, and the room destroy that drops a room's index rows. It is a single
// global counter — deliberately conservative: a message anywhere in the
// account invalidates every cached query, never a stale one.
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
