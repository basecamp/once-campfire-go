package database

import (
	"context"
	"path/filepath"
	"testing"
)

// versionsFixture is one fresh database with the state the audit needs: an
// owner, Alice and Eve as members of a closed room, plus one message in the
// setup room.
type versionsFixture struct {
	db                      *DB
	owner, alice, eve       User
	open, closed, messageID int64
}

func newVersionsFixture(t *testing.T) versionsFixture {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "versions.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	owner, err := db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := db.CreateUser(ctx, "Alice", "alice@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	eve, err := db.CreateUser(ctx, "Eve", "eve@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(ctx, owner.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatalf("setup rooms: %v %d", err, len(rooms))
	}
	closed, err := db.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Quiet", []int64{owner.ID, eve.ID})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, owner.ID, rooms[0].ID, "audit", "<p>hello</p>", "hello")
	if err != nil {
		t.Fatal(err)
	}
	return versionsFixture{db: db, owner: owner, alice: alice, eve: eve, open: rooms[0].ID, closed: closed.ID, messageID: message.ID}
}

// TestSidebarVersionWriteAudit is the ENGINE-20 completeness gate: it lists
// every write helper that changes rows the sidebar template reads (the audit
// list below) and fails if any of them does not move SidebarVersion. Each case
// runs on a fresh database so mutations cannot interfere.
func TestSidebarVersionWriteAudit(t *testing.T) {
	ctx := context.Background()
	assertBumps := func(t *testing.T, mutate func(f versionsFixture) error) {
		t.Helper()
		f := newVersionsFixture(t)
		before, err := f.db.SidebarVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := mutate(f); err != nil {
			t.Fatalf("mutation failed: %v", err)
		}
		after, err := f.db.SidebarVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after == before {
			t.Fatalf("sidebar-visible write did not bump the version (%d == %d)", before, after)
		}
	}
	assertQuiet := func(t *testing.T, mutate func(f versionsFixture) error) {
		t.Helper()
		f := newVersionsFixture(t)
		before, err := f.db.SidebarVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := mutate(f); err != nil {
			t.Fatalf("mutation failed: %v", err)
		}
		after, err := f.db.SidebarVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("non-sidebar write moved the version (%d -> %d)", before, after)
		}
	}

	// ── Audit list: every helper that changes rows the sidebar template
	// reads (rooms, memberships, users, accounts) must bump. ──
	t.Run("room create", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			_, err := f.db.CreateRoom(ctx, f.owner.ID, "Rooms::Direct", "", []int64{f.alice.ID})
			return err
		})
	})
	t.Run("room rename", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			return f.db.UpdateRoom(ctx, f.closed, "Rooms::Closed", "Louder", []int64{f.owner.ID, f.eve.ID})
		})
	})
	t.Run("membership join", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			return f.db.UpdateRoom(ctx, f.closed, "Rooms::Closed", "Quiet", []int64{f.owner.ID, f.alice.ID, f.eve.ID})
		})
	})
	t.Run("membership removal", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			return f.db.UpdateRoom(ctx, f.closed, "Rooms::Closed", "Quiet", []int64{f.owner.ID})
		})
	})
	t.Run("room delete", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error { return f.db.DeleteRoom(ctx, f.closed) })
	})
	t.Run("involvement", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			return f.db.SetInvolvement(ctx, f.eve.ID, f.closed, "invisible")
		})
	})
	t.Run("presence present", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error { return f.db.Presence(ctx, f.eve.ID, f.closed, "present") })
	})
	t.Run("message create", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			_, err := f.db.CreateMessage(ctx, f.owner.ID, f.open, "audit2", "<p>unread</p>", "unread")
			return err
		})
	})
	t.Run("webhook reply", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			_, err := f.db.CreateWebhookReply(ctx, f.owner.ID, f.open, "<p>reply</p>", "reply", 0)
			return err
		})
	})
	t.Run("user create", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			_, err := f.db.CreateUser(ctx, "Eve2", "eve2@test", "digest", "", 0, nil)
			return err
		})
	})
	t.Run("user rename", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			return f.db.UpdateUser(ctx, f.alice.ID, map[string]string{"name": "Allison"}, nil)
		})
	})
	t.Run("user role", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			return f.db.UpdateUser(ctx, f.alice.ID, map[string]string{"role": "1"}, nil)
		})
	})
	t.Run("user deactivate", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error { return f.db.DeactivateUser(ctx, f.alice.ID) })
	})
	t.Run("user ban", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error { return f.db.BanUser(ctx, f.alice.ID, true) })
	})
	t.Run("user unban", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error { return f.db.BanUser(ctx, f.eve.ID, false) })
	})
	t.Run("account restrict", func(t *testing.T) {
		assertBumps(t, func(f versionsFixture) error {
			restrict := true
			return f.db.UpdateAccount(ctx, nil, nil, &restrict, false)
		})
	})
	t.Run("setup", func(t *testing.T) {
		subject, err := Open(filepath.Join(t.TempDir(), "fresh.sqlite3"), 2)
		if err != nil {
			t.Fatal(err)
		}
		defer subject.Close()
		before, err := subject.SidebarVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := subject.Setup(ctx, "Owner", "owner@test", "digest"); err != nil {
			t.Fatal(err)
		}
		after, err := subject.SidebarVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after == before {
			t.Fatalf("setup did not bump the version (%d == %d)", before, after)
		}
	})

	// ── The complement: helpers that write rows the sidebar does not read
	// must NOT move the version. These pin the audit boundary so a future
	// bump that would churn the cache on a non-visible write is a visible
	// change. ──
	t.Run("message edit quiet", func(t *testing.T) {
		assertQuiet(t, func(f versionsFixture) error {
			_, err := f.db.UpdateMessage(ctx, f.owner.ID, f.messageID, "<p>edited</p>", "edited")
			return err
		})
	})
	t.Run("message delete quiet", func(t *testing.T) {
		assertQuiet(t, func(f versionsFixture) error {
			return f.db.DeleteMessage(ctx, f.owner.ID, f.messageID)
		})
	})
	t.Run("boost quiet", func(t *testing.T) {
		assertQuiet(t, func(f versionsFixture) error {
			_, err := f.db.CreateBoost(ctx, f.alice.ID, f.messageID, "congrats")
			return err
		})
	})
	t.Run("search record quiet", func(t *testing.T) {
		assertQuiet(t, func(f versionsFixture) error { return f.db.RecordSearch(ctx, f.owner.ID, "coffee") })
	})
	t.Run("presence absent quiet", func(t *testing.T) {
		assertQuiet(t, func(f versionsFixture) error { return f.db.Presence(ctx, f.eve.ID, f.closed, "absent") })
	})
	t.Run("push subscription quiet", func(t *testing.T) {
		assertQuiet(t, func(f versionsFixture) error {
			return f.db.SavePushSubscription(ctx, f.owner.ID, map[string]*string{
				"endpoint":   strPtr("https://push.test/endpoint"),
				"p256dh_key": strPtr("key"),
				"auth_key":   strPtr("auth"),
			}, "test")
		})
	})
}

func strPtr(s string) *string { return &s }

// TestAuthzVersionWriteAudit is the ENGINE-40b completeness gate: it lists
// every write helper that changes rows the publication authorization query
// (AuthorizedSessions: sessions rows, users.status, membership rows) reads
// and fails if any of them does not move the corresponding generation
// (SessionVersion, UserVersion, MembershipVersion). Each case runs on a
// fresh database so mutations cannot interfere. The complement asserts that
// helpers writing outside those rows move nothing.
func TestAuthzVersionWriteAudit(t *testing.T) {
	ctx := context.Background()
	assert := func(t *testing.T, wantSess, wantUser, wantMember bool, mutate func(f versionsFixture) error, name string) {
		t.Helper()
		f := newVersionsFixture(t)
		s1, u1, m1 := f.db.SessionVersion(), f.db.UserVersion(), f.db.MembershipVersion()
		if err := mutate(f); err != nil {
			t.Fatalf("mutation failed: %v", err)
		}
		s2, u2, m2 := f.db.SessionVersion(), f.db.UserVersion(), f.db.MembershipVersion()
		for _, c := range []struct {
			label  string
			moved  bool
			before int64
			after  int64
		}{{"session", wantSess, s1, s2}, {"user", wantUser, u1, u2}, {"membership", wantMember, m1, m2}} {
			if c.moved && c.after == c.before {
				t.Fatalf("%s: %s write did not move the %s generation (%d == %d)", name, c.label, c.label, c.before, c.after)
			}
			if !c.moved && c.after != c.before {
				t.Fatalf("%s: %s write moved the %s generation (%d -> %d)", name, c.label, c.label, c.before, c.after)
			}
		}
	}

	// ── The audit list: every helper that changes rows AuthorizedSessions
	// reads must move its generation. ──
	for _, tc := range []struct {
		name   string
		sess   bool
		user   bool
		member bool
		mutate func(f versionsFixture) error
	}{
		{"start session", true, false, false, func(f versionsFixture) error {
			_, err := f.db.StartSession(ctx, f.alice.ID, "audit", "127.0.0.1")
			return err
		}},
		{"delete session", true, false, false, func(f versionsFixture) error {
			token, err := f.db.StartSession(ctx, f.alice.ID, "audit", "127.0.0.1")
			if err != nil {
				return err
			}
			return f.db.DeleteSession(ctx, token, f.alice.ID)
		}},
		{"user ban", true, true, false, func(f versionsFixture) error {
			return f.db.BanUser(ctx, f.alice.ID, true)
		}},
		{"user deactivate", true, true, true, func(f versionsFixture) error {
			return f.db.DeactivateUser(ctx, f.alice.ID)
		}},
		{"room create", false, false, true, func(f versionsFixture) error {
			_, err := f.db.CreateRoom(ctx, f.owner.ID, "Rooms::Closed", "New", []int64{f.alice.ID})
			return err
		}},
		{"membership join", false, false, true, func(f versionsFixture) error {
			return f.db.UpdateRoom(ctx, f.closed, "Rooms::Closed", "Quiet", []int64{f.owner.ID, f.alice.ID, f.eve.ID})
		}},
		{"membership removal", false, false, true, func(f versionsFixture) error {
			return f.db.UpdateRoom(ctx, f.closed, "Rooms::Closed", "Quiet", []int64{f.owner.ID})
		}},
		{"room visibility", false, false, true, func(f versionsFixture) error {
			return f.db.UpdateRoom(ctx, f.closed, "Rooms::Open", "Open now", []int64{})
		}},
		{"room delete", false, false, true, func(f versionsFixture) error {
			return f.db.DeleteRoom(ctx, f.closed)
		}},
		{"involvement", false, false, true, func(f versionsFixture) error {
			return f.db.SetInvolvement(ctx, f.eve.ID, f.closed, "invisible")
		}},
		{"user create", false, false, true, func(f versionsFixture) error {
			_, err := f.db.CreateUser(ctx, "Eve2", "eve2@test", "digest", "", 0, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert(t, tc.sess, tc.user, tc.member, tc.mutate, tc.name)
		})
	}

	t.Run("setup", func(t *testing.T) {
		subject, err := Open(filepath.Join(t.TempDir(), "setup-authz.sqlite3"), 2)
		if err != nil {
			t.Fatal(err)
		}
		defer subject.Close()
		s1, u1, m1 := subject.SessionVersion(), subject.UserVersion(), subject.MembershipVersion()
		if _, err := subject.Setup(ctx, "Owner", "owner@test", "digest"); err != nil {
			t.Fatal(err)
		}
		if s2 := subject.SessionVersion(); s2 != s1 {
			t.Fatalf("setup moved the session generation (%d -> %d)", s1, s2)
		}
		if u2 := subject.UserVersion(); u2 != u1 {
			t.Fatalf("setup moved the user generation (%d -> %d)", u1, u2)
		}
		if m2 := subject.MembershipVersion(); m2 == m1 {
			t.Fatalf("setup did not move the membership generation (%d == %d)", m1, m2)
		}
	})

	t.Run("user unban", func(t *testing.T) {
		f := newVersionsFixture(t)
		if err := f.db.BanUser(ctx, f.eve.ID, true); err != nil {
			t.Fatal(err)
		}
		s1, u1, m1 := f.db.SessionVersion(), f.db.UserVersion(), f.db.MembershipVersion()
		if err := f.db.BanUser(ctx, f.eve.ID, false); err != nil {
			t.Fatal(err)
		}
		if s2 := f.db.SessionVersion(); s2 != s1 {
			t.Fatalf("unban moved the session generation (%d -> %d)", s1, s2)
		}
		if u2 := f.db.UserVersion(); u2 == u1 {
			t.Fatalf("unban did not move the user generation (%d == %d)", u1, u2)
		}
		if m2 := f.db.MembershipVersion(); m2 != m1 {
			t.Fatalf("unban moved the membership generation (%d -> %d)", m1, m2)
		}
	})

	t.Run("refresh session quiet", func(t *testing.T) {
		f := newVersionsFixture(t)
		sess, err := f.db.StartSession(ctx, f.alice.ID, "audit", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		s1, u1, m1 := f.db.SessionVersion(), f.db.UserVersion(), f.db.MembershipVersion()
		if _, err := f.db.RefreshSession(ctx, sess, "audit", "127.0.0.1"); err != nil {
			t.Fatal(err)
		}
		if s2 := f.db.SessionVersion(); s2 != s1 {
			t.Fatalf("refresh moved the session generation (%d -> %d)", s1, s2)
		}
		if u2 := f.db.UserVersion(); u2 != u1 {
			t.Fatalf("refresh moved the user generation (%d -> %d)", u1, u2)
		}
		if m2 := f.db.MembershipVersion(); m2 != m1 {
			t.Fatalf("refresh moved the membership generation (%d -> %d)", m1, m2)
		}
	})

	// ── The complement: helpers that write rows the authorization query
	// does not read must move nothing. ──
	for _, tc := range []struct {
		name   string
		mutate func(f versionsFixture) error
	}{
		{"user rename", func(f versionsFixture) error {
			return f.db.UpdateUser(ctx, f.alice.ID, map[string]string{"name": "Allison"}, nil)
		}},
		{"user role", func(f versionsFixture) error {
			return f.db.UpdateUser(ctx, f.alice.ID, map[string]string{"role": "1"}, nil)
		}},
		{"message create", func(f versionsFixture) error {
			_, err := f.db.CreateMessage(ctx, f.owner.ID, f.open, "audit2", "<p>again</p>", "again")
			return err
		}},
		{"message edit", func(f versionsFixture) error {
			_, err := f.db.UpdateMessage(ctx, f.owner.ID, f.messageID, "<p>edited</p>", "edited")
			return err
		}},
		{"message delete", func(f versionsFixture) error {
			return f.db.DeleteMessage(ctx, f.owner.ID, f.messageID)
		}},
		{"boost", func(f versionsFixture) error {
			_, err := f.db.CreateBoost(ctx, f.alice.ID, f.messageID, "thanks")
			return err
		}},
		{"presence present", func(f versionsFixture) error {
			return f.db.Presence(ctx, f.eve.ID, f.closed, "present")
		}},
		{"presence absent", func(f versionsFixture) error {
			return f.db.Presence(ctx, f.eve.ID, f.closed, "absent")
		}},
		{"search record", func(f versionsFixture) error {
			return f.db.RecordSearch(ctx, f.owner.ID, "coffee")
		}},
		{"push subscription", func(f versionsFixture) error {
			return f.db.SavePushSubscription(ctx, f.owner.ID, map[string]*string{
				"endpoint":   strPtr("https://push.test/endpoint"),
				"p256dh_key": strPtr("key"),
				"auth_key":   strPtr("auth"),
			}, "test")
		}},
	} {
		t.Run(tc.name+" quiet", func(t *testing.T) {
			assert(t, false, false, false, tc.mutate, tc.name)
		})
	}
}

// TestSidebarVersionSeedStable pins the seeding contract: first use reads one
// fingerprint from the database, repeated calls are stable, and two databases
// with identical sidebar-relevant rows (frozen clock) seed identically while
// an extra user changes the seed.
func TestSidebarVersionSeedStable(t *testing.T) {
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	ctx := context.Background()
	open := func(t *testing.T) *DB {
		t.Helper()
		db, err := Open(filepath.Join(t.TempDir(), "seed.sqlite3"), 2)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if _, err := db.Setup(ctx, "Owner", "owner@test", "digest"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.CreateUser(ctx, "Alice", "alice@test", "digest", "", 0, nil); err != nil {
			t.Fatal(err)
		}
		return db
	}
	first, err := open(t).SidebarVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := open(t).SidebarVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("identical databases seeded differently: %d != %d", first, second)
	}
	older, err := open(t).SidebarVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// An additional row before first use must change the seed.
	db := open(t)
	if _, err := db.CreateUser(ctx, "Eve", "eve@test", "digest", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	extra, err := db.SidebarVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if extra == older {
		t.Fatalf("pre-seed write did not change the version (%d)", extra)
	}
	// Repeated calls without writes are stable.
	again, err := db.SidebarVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again != extra {
		t.Fatalf("version drifted without writes: %d != %d", extra, again)
	}
}
