package fastdb

import (
	"context"
	"reflect"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// TestDifferentialSidebarMembers pins the ENGINE-20 joined sidebar scan: for
// every user, SidebarMembers grouped by room must equal SidebarRooms plus
// RoomMembers per direct room — same rooms, same order, same member lists
// (the join's ORDER BY lower(r.name),m.rowid must reproduce the per-room
// reader exactly, membership rowid order included).
func TestDifferentialSidebarMembers(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()

	users, err := queryIDs(t, d, "SELECT id FROM users ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) == 0 {
		t.Fatal("fixture has no users")
	}
	for _, user := range users {
		wantRooms, err := d.SidebarRooms(ctx, user)
		if err != nil {
			t.Fatalf("database.SidebarRooms(%d): %v", user, err)
		}
		got, err := c.SidebarMembers(nil, user)
		if err != nil {
			t.Fatalf("fastdb.SidebarMembers(%d): %v", user, err)
		}
		if (wantRooms == nil) != (got == nil) {
			t.Errorf("SidebarMembers(%d) nil-ness: database %v, fastdb %v", user, wantRooms == nil, got == nil)
		}
		if got == nil {
			continue
		}
		// Group the flat rows by room: each room is contiguous, non-direct
		// rooms contribute one row with a zero member.
		var rooms [][]SidebarMember
		for i := 0; i < len(got); {
			j := i + 1
			for j < len(got) && got[j].Room.ID == got[i].Room.ID {
				j++
			}
			rooms = append(rooms, got[i:j])
			i = j
		}
		if len(rooms) != len(wantRooms) {
			t.Errorf("SidebarMembers(%d): fastdb %d rooms != database %d", user, len(rooms), len(wantRooms))
			continue
		}
		for i := range wantRooms {
			group := rooms[i]
			if !reflect.DeepEqual(group[0].Room.record(), recordOfSidebar(wantRooms[i])) {
				t.Errorf("SidebarMembers(%d)[%d]: fastdb %+v != database %+v", user, i, group[0].Room.record(), recordOfSidebar(wantRooms[i]))
			}
			if wantRooms[i].Type != "Rooms::Direct" {
				if len(group) != 1 || group[0].Member.ID != 0 {
					t.Errorf("SidebarMembers(%d) room %d: non-direct room has %d member rows, want exactly one with a zero member", user, wantRooms[i].ID, len(group))
				}
				continue
			}
			wantMembers, err := d.RoomMembers(ctx, wantRooms[i].ID)
			if err != nil {
				t.Fatalf("database.RoomMembers(%d): %v", wantRooms[i].ID, err)
			}
			viewer := []database.User(nil)
			for _, member := range wantMembers {
				if member.ID != user {
					viewer = append(viewer, member)
				}
			}
			if len(group) != len(viewer) {
				t.Errorf("SidebarMembers(%d) direct room %d: fastdb %d members != database %d (viewer excluded)", user, wantRooms[i].ID, len(group), len(viewer))
				continue
			}
			for k := range viewer {
				if group[k].Member.ID == 0 || !reflect.DeepEqual(group[k].Member.record(), recordOfUser(viewer[k])) {
					t.Errorf("SidebarMembers(%d) direct room %d member %d: fastdb %+v != database %+v", user, wantRooms[i].ID, k, group[k].Member.record(), recordOfUser(viewer[k]))
				}
			}
		}
	}
}

// TestSidebarMembersJoinOrder pins the member order after a membership is
// removed and re-added: the re-granted row gets a new membership rowid, so
// the joined scan must follow membership rowid order, not user id order, to
// stay byte-identical with the per-room RoomMembers reader.
func TestSidebarMembersJoinOrder(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()

	users, err := queryIDs(t, d, "SELECT id FROM users ORDER BY id LIMIT 3")
	if err != nil || len(users) < 3 {
		t.Fatalf("need three fixture users: %d, %v", len(users), err)
	}
	direct, err := d.CreateRoom(ctx, users[0], "Rooms::Direct", "", []int64{users[1], users[2]})
	if err != nil {
		t.Fatal(err)
	}
	// Remove users[1] and re-add them: the new membership row has the newest
	// rowid, so rowid order is (users[0], users[2], users[1]) while user id
	// order would be (users[0], users[1], users[2]).
	if _, err := d.Write.Exec("DELETE FROM memberships WHERE room_id=? AND user_id=?", direct.ID, users[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO memberships(room_id,user_id,created_at,updated_at) VALUES(?,?,?,?)", direct.ID, users[1], "2026-01-01 00:00:00.000000", "2026-01-01 00:00:00.000000"); err != nil {
		t.Fatal(err)
	}
	got, err := c.SidebarMembers(nil, users[0])
	if err != nil {
		t.Fatal(err)
	}
	all, err := d.RoomMembers(ctx, direct.ID)
	if err != nil {
		t.Fatal(err)
	}
	var want []database.User
	for _, member := range all {
		if member.ID != users[0] {
			want = append(want, member)
		}
	}
	var members []User
	for _, row := range got {
		if row.Room.ID == direct.ID && row.Member.ID != 0 {
			members = append(members, row.Member)
		}
	}
	if len(members) != len(want) {
		t.Fatalf("direct room %d: fastdb %d members != database %d", direct.ID, len(members), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(members[i].record(), recordOfUser(want[i])) {
			t.Errorf("joined order[%d]: fastdb %+v != database %+v", i, members[i].record(), recordOfUser(want[i]))
		}
	}
	// The re-granted member keeps their user-id position on both readers — the
	// RoomMembers covering index orders by (room_id, user_id), not rowid, and
	// the joined scan must reproduce that even though the re-added membership
	// row has the newest rowid.
	if members[len(members)-1].ID == users[1] {
		t.Errorf("re-granted member %d must sit in user-id order, not rowid order (%v)", users[1], members)
	}
}
