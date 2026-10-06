package fastdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// The differential tests run every fastdb query against the same database
// file the legacy database/sql readers use and compare decoded values field by
// field. The readable fixture is the Rust parity seed when it is checked out
// on this machine; otherwise populateFallback builds a database with the same
// shapes (rooms, memberships with invisible/unread state, messages with and
// without rich text, sessions).
//
// Fixture contract used by the benchmark as well: at least one room holds 40 or
// more messages.

// seedFixture returns the parity seed's path, or "" when it is not available.
// CAMPFIRE_SEED_DB overrides the lookup; CAMPFIRE_SEED_DB=off forces the
// synthetic fallback (used to exercise that path).
func seedFixture() string {
	if path := os.Getenv("CAMPFIRE_SEED_DB"); path != "" {
		if path == "off" {
			return ""
		}
		return path
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	path := filepath.Join(dir, "..", "once-campfire-rust", "parity", ".seed", "default", "db", "production.sqlite3")
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	return path
}

// copyFixture copies the seed and its WAL into the test's temp dir so the
// original is never opened for writing and never modified.
func copyFixture(t testing.TB, src, dst string) {
	t.Helper()
	copyOne := func(from, to string) {
		in, err := os.Open(from)
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		out, err := os.Create(to)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
	}
	copyOne(src, dst)
	if _, err := os.Stat(src + "-wal"); err == nil {
		copyOne(src+"-wal", dst+"-wal")
	}
}

// fixtureDB returns a database path for the differential and benchmark tests.
// The parity seed is copied (never opened for writing in place); opening the
// copy through database.Open applies the same preparation the application
// performs — the migration check and the (room_id, created_at) index — so the
// benchmark measures the production schema.
func fixtureDB(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.sqlite3")
	seed := seedFixture()
	if seed != "" {
		copyFixture(t, seed, path)
	}
	d, err := database.Open(path, 2)
	if err != nil {
		if strings.Contains(err.Error(), "fts5") {
			t.Skipf("fastdb synthetic fixture needs -tags sqlite_fts5 or CAMPFIRE_SEED_DB pointing at the parity seed: %v", err)
		}
		t.Fatal(err)
	}
	if seed == "" {
		populateFallback(t, d)
	}
	d.Close()
	return path
}

// populateFallback writes the shapes the differential covers: an open room
// with 48 messages (rich text on every third), a direct room, a closed room
// with an invisible member, an unread membership, and a session.
func populateFallback(t testing.TB, d *database.DB) {
	t.Helper()
	ctx := context.Background()
	alice, err := d.Setup(ctx, "Alice", "alice@example.test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := d.CreateUser(ctx, "Bob", "bob@example.test", "digest", "Bobby bio", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	carol, err := d.CreateUser(ctx, "Carol", "carol@example.test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateRoom(ctx, alice.ID, "Rooms::Direct", "", []int64{bob.ID}); err != nil {
		t.Fatal(err)
	}
	closed, err := d.CreateRoom(ctx, alice.ID, "Rooms::Closed", "War Room", []int64{bob.ID, carol.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetInvolvement(ctx, carol.ID, closed.ID, "invisible"); err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	open := int64(0)
	for _, room := range rooms {
		if room.Type == "Rooms::Open" {
			open = room.ID
		}
	}
	if open == 0 {
		t.Fatal("fixture has no open room")
	}
	base := time.Date(2026, 1, 2, 15, 0, 0, 0, time.UTC)
	for i := 0; i < 48; i++ {
		creator := alice.ID
		if i%2 == 1 {
			creator = bob.ID
		}
		body, plain := "", ""
		if i%3 == 0 {
			body = fmt.Sprintf("<p>message %d</p>", i)
			plain = fmt.Sprintf("message %d", i)
		}
		m, err := d.CreateMessage(ctx, creator, open, "", body, plain)
		if err != nil {
			t.Fatal(err)
		}
		stamp := database.Stamp(base.Add(time.Duration(i) * time.Minute))
		updated := database.Stamp(base.Add(time.Duration(i) * time.Hour))
		if _, err := d.Write.Exec("UPDATE messages SET created_at=?,updated_at=? WHERE id=?", stamp, updated, m.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Write.Exec("UPDATE memberships SET unread_at=? WHERE room_id=? AND user_id=?", database.Stamp(base.Add(2*time.Hour)), open, bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.StartSession(ctx, alice.ID, "test-agent", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
}

func openBoth(t *testing.T, path string) (*database.DB, *Conn) {
	t.Helper()
	d, err := database.Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, err := OpenReadOnly(path, 256)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return d, c
}

// Plain records make reflect.DeepEqual the comparison; both readers decode
// the same SQLite values, so any difference is a decoding difference.

type roomRecord struct {
	ID, CreatorID int64
	Name, Type    string
	UpdatedAt     time.Time
}

func (r Room) record() roomRecord {
	return roomRecord{ID: r.ID, CreatorID: r.CreatorID, Name: r.Name, Type: r.Type, UpdatedAt: r.UpdatedAt}
}
func recordOfRoom(r database.Room) roomRecord {
	return roomRecord{ID: r.ID, CreatorID: r.CreatorID, Name: r.Name, Type: r.Type, UpdatedAt: r.UpdatedAt}
}

type messageRecord struct {
	ID, RoomID, CreatorID   int64
	ClientID, Body, Creator string
	CreatedAt, UpdatedAt    time.Time
}

func (m Message) record() messageRecord {
	return messageRecord{ID: m.ID, RoomID: m.RoomID, CreatorID: m.CreatorID, ClientID: m.ClientID, Body: m.Body, Creator: m.Creator, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}
func recordOfMessage(m database.Message) messageRecord {
	return messageRecord{ID: m.ID, RoomID: m.RoomID, CreatorID: m.CreatorID, ClientID: m.ClientID, Body: m.Body, Creator: m.Creator, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

type userRecord struct {
	ID                    int64
	Name, Email, Password string
	Bio, BotToken         string
	UpdatedAt             time.Time
	Role, Status          int
}

func (u User) record() userRecord {
	return userRecord{ID: u.ID, Name: u.Name, Email: u.Email, Password: u.Password, Bio: u.Bio, BotToken: u.BotToken, UpdatedAt: u.UpdatedAt, Role: u.Role, Status: u.Status}
}
func recordOfUser(u database.User) userRecord {
	return userRecord{ID: u.ID, Name: u.Name, Email: u.Email, Password: u.Password, Bio: u.Bio, BotToken: u.BotToken, UpdatedAt: u.UpdatedAt, Role: u.Role, Status: u.Status}
}

type sidebarRecord struct {
	ID, CreatorID int64
	Name, Type    string
	UpdatedAt     time.Time
	Involvement   string
	Unread        bool
}

func (r SidebarRoom) record() sidebarRecord {
	return sidebarRecord{ID: r.ID, CreatorID: r.CreatorID, Name: r.Name, Type: r.Type, UpdatedAt: r.UpdatedAt, Involvement: r.Involvement, Unread: r.Unread}
}
func recordOfSidebar(r database.SidebarRoom) sidebarRecord {
	return sidebarRecord{ID: r.ID, CreatorID: r.CreatorID, Name: r.Name, Type: r.Type, UpdatedAt: r.UpdatedAt, Involvement: r.Involvement, Unread: r.Unread}
}

func queryPairs(t *testing.T, d *database.DB, query string) [][2]int64 {
	t.Helper()
	ctx := context.Background()
	rows, err := d.Read.QueryContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var pairs [][2]int64
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, [2]int64{a, b})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return pairs
}

func TestDifferentialRoomAndMembership(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()

	pairs := queryPairs(t, d, "SELECT user_id,room_id FROM memberships ORDER BY room_id,user_id")
	if len(pairs) == 0 {
		t.Fatal("fixture has no memberships")
	}
	for _, pair := range pairs {
		user, room := pair[0], pair[1]
		want, err := d.Room(ctx, user, room)
		if err != nil {
			t.Fatalf("database.Room(%d,%d): %v", user, room, err)
		}
		var got Room
		if err := c.Room(&got, user, room); err != nil {
			t.Fatalf("fastdb.Room(%d,%d): %v", user, room, err)
		}
		if !reflect.DeepEqual(got.record(), recordOfRoom(want)) {
			t.Errorf("Room(%d,%d): fastdb %+v != database %+v", user, room, got.record(), recordOfRoom(want))
		}
		wantInvolvement, err := d.Involvement(ctx, user, room)
		if err != nil {
			t.Fatalf("database.Involvement(%d,%d): %v", user, room, err)
		}
		gotInvolvement, err := c.Involvement(user, room)
		if err != nil || gotInvolvement != wantInvolvement {
			t.Errorf("Involvement(%d,%d): fastdb %q/%v != database %q", user, room, gotInvolvement, err, wantInvolvement)
		}
	}

	const missing = int64(1) << 62
	var room Room
	if err := c.Room(&room, missing, missing); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing room: got %v, want ErrNoRows", err)
	}
	if _, err := c.Involvement(missing, missing); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing involvement: got %v, want ErrNoRows", err)
	}
}

func TestDifferentialSessionUser(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()

	rows, err := d.Read.QueryContext(ctx, "SELECT token FROM sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	sessions := 0
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			t.Fatal(err)
		}
		sessions++
		want, err := d.SessionUser(ctx, token)
		if err != nil {
			t.Fatalf("database.SessionUser: %v", err)
		}
		var got User
		if err := c.SessionUser(&got, token); err != nil {
			t.Fatalf("fastdb.SessionUser: %v", err)
		}
		if !reflect.DeepEqual(got.record(), recordOfUser(want)) {
			t.Errorf("SessionUser(%q): fastdb %+v != database %+v", token, got.record(), recordOfUser(want))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if sessions == 0 {
		t.Fatal("fixture has no sessions")
	}
	var missing User
	if err := c.SessionUser(&missing, "no-such-token"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing session: got %v, want ErrNoRows", err)
	}
}

func TestDifferentialSidebarRooms(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()

	// Every user, including those with no memberships.
	rows, err := d.Read.QueryContext(ctx, "SELECT id FROM users ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var users []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		users = append(users, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(users) == 0 {
		t.Fatal("fixture has no users")
	}
	for _, user := range users {
		want, err := d.SidebarRooms(ctx, user)
		if err != nil {
			t.Fatalf("database.SidebarRooms(%d): %v", user, err)
		}
		got, err := c.SidebarRooms(nil, user)
		if err != nil {
			t.Fatalf("fastdb.SidebarRooms(%d): %v", user, err)
		}
		if len(got) != len(want) {
			t.Errorf("SidebarRooms(%d): fastdb %d rows != database %d", user, len(got), len(want))
			continue
		}
		for i := range want {
			if !reflect.DeepEqual(got[i].record(), recordOfSidebar(want[i])) {
				t.Errorf("SidebarRooms(%d)[%d]: fastdb %+v != database %+v", user, i, got[i].record(), recordOfSidebar(want[i]))
			}
		}
	}
}

func TestDifferentialMessagePages(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()

	// Room ids are ordered so a failure names the same room on every run.
	roomRows, err := d.Read.QueryContext(ctx, "SELECT DISTINCT room_id FROM messages ORDER BY room_id")
	if err != nil {
		t.Fatal(err)
	}
	var rooms []int64
	for roomRows.Next() {
		var id int64
		if err := roomRows.Scan(&id); err != nil {
			roomRows.Close()
			t.Fatal(err)
		}
		rooms = append(rooms, id)
	}
	if err := roomRows.Err(); err != nil {
		roomRows.Close()
		t.Fatal(err)
	}
	roomRows.Close()
	if len(rooms) == 0 {
		t.Fatal("fixture has no messages")
	}

	compareRefs := func(t *testing.T, room, anchor int64) {
		t.Helper()
		want, err := d.MessagePageReferences(ctx, room, anchor, "before")
		if err != nil {
			t.Fatalf("database.MessagePageReferences(%d,%d,before): %v", room, anchor, err)
		}
		got, err := c.MessageRefs(nil, room, anchor)
		if err != nil {
			t.Fatalf("fastdb.MessageRefs(%d,%d): %v", room, anchor, err)
		}
		if len(got) != len(want) {
			t.Fatalf("MessageRefs(%d,%d): fastdb %d rows != database %d", room, anchor, len(got), len(want))
		}
		for i := range want {
			if got[i].ID != want[i].ID || got[i].RoomID != want[i].RoomID || got[i].UpdatedAt != want[i].UpdatedAt {
				t.Errorf("MessageRefs(%d,%d)[%d]: fastdb %+v != database {%d %d %v}", room, anchor, i, got[i], want[i].ID, want[i].RoomID, want[i].UpdatedAt)
			}
		}
	}

	compareMessages := func(t *testing.T, label string, room, anchor int64, direction string, want []database.Message, got []Message) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s(%d,%d,%s): fastdb %d rows != database %d", label, room, anchor, direction, len(got), len(want))
		}
		for i := range want {
			if !reflect.DeepEqual(got[i].record(), recordOfMessage(want[i])) {
				t.Errorf("%s(%d,%d,%s)[%d]: fastdb %+v != database %+v", label, room, anchor, direction, i, got[i].record(), recordOfMessage(want[i]))
			}
		}
	}
	comparePage := func(t *testing.T, room, anchor int64, direction string, want []database.Message, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("database.MessagePage(%d,%d,%s): %v", room, anchor, direction, err)
		}
		got, err := c.MessagePage(nil, room, anchor, direction)
		if err != nil {
			t.Fatalf("fastdb.MessagePage(%d,%d,%s): %v", room, anchor, direction, err)
		}
		compareMessages(t, "MessagePage", room, anchor, direction, want, got)
	}
	compareReferences := func(t *testing.T, room, anchor int64, direction string, want []database.Message, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("database.MessagePageReferences(%d,%d,%s): %v", room, anchor, direction, err)
		}
		got, err := c.MessagePageReferences(nil, room, anchor, direction)
		if err != nil {
			t.Fatalf("fastdb.MessagePageReferences(%d,%d,%s): %v", room, anchor, direction, err)
		}
		compareMessages(t, "MessagePageReferences", room, anchor, direction, want, got)
	}

	for _, room := range rooms {
		ids, err := queryIDs(t, d, "SELECT id FROM messages WHERE room_id=? ORDER BY created_at,id", room)
		if err != nil {
			t.Fatal(err)
		}
		anchors := []int64{0}
		if len(ids) > 0 {
			anchors = append(anchors, ids[0], ids[len(ids)/2], ids[len(ids)-1])
		}
		seen := map[int64]bool{}
		for _, anchor := range anchors {
			if seen[anchor] {
				continue
			}
			seen[anchor] = true
			compareRefs(t, room, anchor)
			wantRefs, err := d.MessagePageReferences(ctx, room, anchor, "before")
			compareReferences(t, room, anchor, "before", wantRefs, err)
			wantBefore, err := d.MessagePage(ctx, room, anchor, "before")
			comparePage(t, room, anchor, "before", wantBefore, err)
			if anchor == 0 {
				continue
			}
			wantAfter, err := d.MessagePage(ctx, room, anchor, "after")
			comparePage(t, room, anchor, "after", wantAfter, err)
			wantAround, err := d.MessagePage(ctx, room, anchor, "around")
			comparePage(t, room, anchor, "around", wantAround, err)
			// MessagePageReferences must match MessagePage where it delegates.
			wantRefAfter, err := d.MessagePageReferences(ctx, room, anchor, "after")
			compareReferences(t, room, anchor, "after", wantRefAfter, err)
			wantRefAround, err := d.MessagePageReferences(ctx, room, anchor, "around")
			compareReferences(t, room, anchor, "around", wantRefAround, err)
		}
	}

	// Unknown rooms return empty pages, exactly like the database reader.
	const missing = int64(1) << 62
	if got, err := c.MessageRefs(nil, missing, 0); err != nil || len(got) != 0 {
		t.Errorf("unknown room refs: %v rows=%d", err, len(got))
	}
	if got, err := c.MessagePage(nil, missing, 0, "before"); err != nil || len(got) != 0 {
		t.Errorf("unknown room page: %v rows=%d", err, len(got))
	}

	// A valid room with an unknown anchor: the reduced and before windows are
	// empty without error; after/around fail the stamp lookup on both readers.
	room := rooms[0]
	if got, err := c.MessageRefs(nil, room, missing); err != nil || len(got) != 0 {
		t.Errorf("unknown anchor refs: %v rows=%d", err, len(got))
	}
	wantBefore, err := d.MessagePage(ctx, room, missing, "before")
	comparePage(t, room, missing, "before", wantBefore, err)
	wantRefs, err := d.MessagePageReferences(ctx, room, missing, "before")
	compareReferences(t, room, missing, "before", wantRefs, err)
	if _, err := d.MessagePage(ctx, room, missing, "after"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("database missing anchor after: %v", err)
	}
	if _, err := c.MessagePage(nil, room, missing, "after"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("fastdb missing anchor after: %v", err)
	}
	if _, err := d.MessagePage(ctx, room, missing, "around"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("database missing anchor around: %v", err)
	}
	if _, err := c.MessagePage(nil, room, missing, "around"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("fastdb missing anchor around: %v", err)
	}
}

// TestDifferentialInvitation pins the room-page invitation probe (the raw SQL
// internal/web/server.go runs) and forces its true branch by trimming the
// account's first room to 40 messages on a private copy.
func TestDifferentialInvitation(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()

	invitation := func(room int64) (bool, error) {
		var want bool
		err := d.Read.QueryRowContext(ctx, "SELECT ?=(SELECT id FROM rooms ORDER BY created_at LIMIT 1) AND NOT EXISTS(SELECT 1 FROM messages WHERE room_id=? LIMIT 1 OFFSET 40)", room, room).Scan(&want)
		return want, err
	}

	roomIDs, err := queryIDs(t, d, "SELECT id FROM rooms ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if len(roomIDs) == 0 {
		t.Fatal("fixture has no rooms")
	}
	for _, room := range roomIDs {
		want, err := invitation(room)
		if err != nil {
			t.Fatalf("database invitation(%d): %v", room, err)
		}
		got, err := c.Invitation(room)
		if err != nil {
			t.Fatalf("fastdb invitation(%d): %v", room, err)
		}
		if got != want {
			t.Errorf("Invitation(%d) = %v, want %v", room, got, want)
		}
	}

	var first int64
	if err := d.Read.QueryRowContext(ctx, "SELECT id FROM rooms ORDER BY created_at LIMIT 1").Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("DELETE FROM messages WHERE room_id=? AND id NOT IN (SELECT id FROM messages WHERE room_id=? ORDER BY created_at DESC LIMIT 40)", first, first); err != nil {
		t.Fatal(err)
	}
	want, err := invitation(first)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Invitation(first)
	if err != nil {
		t.Fatal(err)
	}
	if want != got {
		t.Errorf("trimmed first room: fastdb %v != database %v", got, want)
	}
	if !want {
		t.Errorf("trimmed first room: invitation = false, want true")
	}

	const missing = int64(1) << 62
	want, err = invitation(missing)
	if err != nil {
		t.Fatal(err)
	}
	if got, err = c.Invitation(missing); err != nil {
		t.Fatal(err)
	}
	if want != got {
		t.Errorf("missing room: fastdb %v != database %v", got, want)
	}
}

// TestDifferentialRoomMembers pins database.DB.RoomMembers, including its
// non-nil empty slice for a room with no members.
func TestDifferentialRoomMembers(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()

	rooms, err := queryIDs(t, d, "SELECT id FROM rooms ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) == 0 {
		t.Fatal("fixture has no rooms")
	}
	members := make([]User, 0, 8)
	for _, room := range rooms {
		want, err := d.RoomMembers(ctx, room)
		if err != nil {
			t.Fatalf("database.RoomMembers(%d): %v", room, err)
		}
		got, err := c.RoomMembers(members[:0], room)
		if err != nil {
			t.Fatalf("fastdb.RoomMembers(%d): %v", room, err)
		}
		if (want == nil) != (got == nil) {
			t.Errorf("RoomMembers(%d) nil-ness: database %v, fastdb %v", room, want == nil, got == nil)
		}
		if len(got) != len(want) {
			t.Errorf("RoomMembers(%d): fastdb %d rows != database %d", room, len(got), len(want))
			continue
		}
		for i := range want {
			if !reflect.DeepEqual(got[i].record(), recordOfUser(want[i])) {
				t.Errorf("RoomMembers(%d)[%d]: fastdb %+v != database %+v", room, i, got[i].record(), recordOfUser(want[i]))
			}
		}
		members = got
	}

	const missing = int64(1) << 62
	want, err := d.RoomMembers(ctx, missing)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.RoomMembers(nil, missing)
	if err != nil {
		t.Fatal(err)
	}
	if want == nil || got == nil {
		t.Errorf("empty RoomMembers must be non-nil: database %v, fastdb %v", want == nil, got == nil)
	}
	if len(want) != 0 || len(got) != 0 {
		t.Errorf("empty RoomMembers has rows: database %d, fastdb %d", len(want), len(got))
	}
}

// TestDifferentialDecodeErrors pins the decode-error shapes. A row whose
// column cannot be decoded drops the partial slice on both readers
// (scanMessages-style nil). A clean mid-scan step error cannot be injected —
// csqlite exposes no SQLite interrupt hook and no mirrored query has a
// row-dependent failure — so the rows.Err() partial-slice path is not tested
// here rather than faked.
func TestDifferentialDecodeErrors(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()

	var room int64
	if err := d.Read.QueryRowContext(ctx, "SELECT room_id FROM messages ORDER BY id LIMIT 1").Scan(&room); err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := d.Read.QueryRowContext(ctx, "SELECT id FROM users ORDER BY id LIMIT 1").Scan(&user); err != nil {
		t.Fatal(err)
	}

	// 1. Invalid timestamp: updated_at is decoded by both readers.
	res, err := d.Write.Exec("INSERT INTO messages(client_message_id,creator_id,room_id,created_at,updated_at) VALUES('bad-stamp',?,?,'9999-01-01 00:00:00','not a timestamp')", user, room)
	if err != nil {
		t.Fatal(err)
	}
	badID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	wantRefs, wantRefsErr := d.MessagePageReferences(ctx, room, 0, "before")
	gotRefs, gotRefsErr := c.MessageRefs(nil, room, 0)
	if wantRefsErr == nil || gotRefsErr == nil {
		t.Fatalf("bad timestamp refs: want errors, got database=%v fastdb=%v", wantRefsErr, gotRefsErr)
	}
	if wantRefs != nil || gotRefs != nil {
		t.Errorf("bad timestamp refs: want nil slices, got database nil=%v fastdb nil=%v", wantRefs == nil, gotRefs == nil)
	}
	wantPage, wantPageErr := d.MessagePage(ctx, room, 0, "before")
	gotPage, gotPageErr := c.MessagePage(nil, room, 0, "before")
	if wantPageErr == nil || gotPageErr == nil {
		t.Fatalf("bad timestamp page: want errors, got database=%v fastdb=%v", wantPageErr, gotPageErr)
	}
	if wantPage != nil || gotPage != nil {
		t.Errorf("bad timestamp page: want nil slices, got database nil=%v fastdb nil=%v", wantPage == nil, gotPage == nil)
	}
	if _, err := d.Write.Exec("DELETE FROM messages WHERE id=?", badID); err != nil {
		t.Fatal(err)
	}

	// 2. TEXT stored in the INTEGER-affinity creator_id column. The writer
	// pool is a single connection, so the pragma applies to the insert; both
	// readers must refuse the coercion rather than return digits.
	if _, err := d.Write.Exec("PRAGMA foreign_keys=off"); err != nil {
		t.Fatal(err)
	}
	_, err = d.Write.Exec("INSERT INTO messages(client_message_id,creator_id,room_id,created_at,updated_at) VALUES('bad-int','not-an-int',?,'9999-01-02 00:00:00','9999-01-02 00:00:00')", room)
	if _, ferr := d.Write.Exec("PRAGMA foreign_keys=on"); ferr != nil {
		t.Fatal(ferr)
	}
	if err != nil {
		t.Fatal(err)
	}
	wantInt, wantIntErr := d.MessagePage(ctx, room, 0, "before")
	gotInt, gotIntErr := c.MessagePage(nil, room, 0, "before")
	if wantIntErr == nil || gotIntErr == nil {
		t.Fatalf("text in integer: want errors, got database=%v fastdb=%v", wantIntErr, gotIntErr)
	}
	if wantInt != nil || gotInt != nil {
		t.Errorf("text in integer: want nil slices, got database nil=%v fastdb nil=%v", wantInt == nil, gotInt == nil)
	}

	// 3. A room whose updated_at cannot be decoded: both readers return the
	// zero Room, and fastdb must zero a pre-filled destination.
	res, err = d.Write.Exec("INSERT INTO rooms(name,type,creator_id,created_at,updated_at) VALUES('Bad Room','Rooms::Open',?,'2026-01-01 00:00:00','not a timestamp')", user)
	if err != nil {
		t.Fatal(err)
	}
	badRoom, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO memberships(room_id,user_id,created_at,updated_at) VALUES(?,?,'2026-01-01 00:00:00','2026-01-01 00:00:00')", badRoom, user); err != nil {
		t.Fatal(err)
	}
	wantRoom, wantRoomErr := d.Room(ctx, user, badRoom)
	if wantRoomErr == nil {
		t.Fatal("database.Room decoded an invalid timestamp")
	}
	// database/sql's Scan fills columns left to right until the failing one,
	// so both readers return the same partially populated Room.
	if wantRoom.ID != badRoom || wantRoom.Name != "Bad Room" || !wantRoom.UpdatedAt.IsZero() {
		t.Fatalf("database.Room partial shape changed: %+v", wantRoom)
	}
	gotRoom := Room{ID: 12345, CreatorID: 999, Name: "sentinel", Type: "sentinel"}
	if err := c.Room(&gotRoom, user, badRoom); err == nil {
		t.Fatal("fastdb.Room decoded an invalid timestamp")
	}
	if !reflect.DeepEqual(gotRoom.record(), recordOfRoom(wantRoom)) {
		t.Errorf("Room error shape: fastdb %+v != database %+v", gotRoom.record(), recordOfRoom(wantRoom))
	}

	// The same zeroing holds for the other single-row lookup.
	wantUser, err := d.SessionUser(ctx, "no-such-token")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if wantUser != (database.User{}) {
		t.Errorf("database.SessionUser returned %+v on error, want zero", wantUser)
	}
	gotUser := User{ID: 7, Name: "sentinel"}
	if err := c.SessionUser(&gotUser, "no-such-token"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if gotUser != (User{}) {
		t.Errorf("fastdb.SessionUser left dst %+v on error, want zero", gotUser)
	}
}

// TestDifferentialEmptyShapes pins nil-ness: the reduced reference path uses a
// nil accumulator in internal/database and stays nil; the full message paths
// and RoomMembers use non-nil accumulators and must return a non-nil empty
// slice.
func TestDifferentialEmptyShapes(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()
	const missing = int64(1) << 62

	wantRefs, err := d.MessagePageReferences(ctx, missing, 0, "before")
	if err != nil {
		t.Fatal(err)
	}
	gotRefs, err := c.MessageRefs(nil, missing, 0)
	if err != nil {
		t.Fatal(err)
	}
	if wantRefs != nil || gotRefs != nil {
		t.Errorf("empty reduced refs: want nil/nil, got database nil=%v fastdb nil=%v", wantRefs == nil, gotRefs == nil)
	}
	if len(wantRefs) != 0 || len(gotRefs) != 0 {
		t.Errorf("empty reduced refs have rows: database %d fastdb %d", len(wantRefs), len(gotRefs))
	}

	wantPage, err := d.MessagePage(ctx, missing, 0, "before")
	if err != nil {
		t.Fatal(err)
	}
	gotPage, err := c.MessagePage(nil, missing, 0, "before")
	if err != nil {
		t.Fatal(err)
	}
	if wantPage == nil || gotPage == nil {
		t.Errorf("empty full page must be non-nil: database nil=%v fastdb nil=%v", wantPage == nil, gotPage == nil)
	}

	// An after window that matches nothing, relative to a real anchor.
	var room, anchor int64
	if err := d.Read.QueryRowContext(ctx, "SELECT room_id FROM messages ORDER BY id LIMIT 1").Scan(&room); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRowContext(ctx, "SELECT id FROM messages WHERE room_id=? ORDER BY created_at DESC LIMIT 1", room).Scan(&anchor); err != nil {
		t.Fatal(err)
	}
	wantAfter, err := d.MessagePage(ctx, room, anchor, "after")
	if err != nil {
		t.Fatal(err)
	}
	gotAfter, err := c.MessagePage(nil, room, anchor, "after")
	if err != nil {
		t.Fatal(err)
	}
	if len(wantAfter) != 0 || len(gotAfter) != 0 {
		t.Fatalf("after window has rows: database %d fastdb %d", len(wantAfter), len(gotAfter))
	}
	if wantAfter == nil || gotAfter == nil {
		t.Errorf("empty after window must be non-nil: database nil=%v fastdb nil=%v", wantAfter == nil, gotAfter == nil)
	}

	// A user with no memberships: the sidebar accumulator is nil.
	wantSidebar, err := d.SidebarRooms(ctx, missing)
	if err != nil {
		t.Fatal(err)
	}
	gotSidebar, err := c.SidebarRooms(nil, missing)
	if err != nil {
		t.Fatal(err)
	}
	if wantSidebar != nil || gotSidebar != nil {
		t.Errorf("empty sidebar: want nil/nil, got database nil=%v fastdb nil=%v", wantSidebar == nil, gotSidebar == nil)
	}
}

func queryIDs(t *testing.T, d *database.DB, query string, args ...any) ([]int64, error) {
	t.Helper()
	rows, err := d.Read.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
