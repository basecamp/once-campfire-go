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
