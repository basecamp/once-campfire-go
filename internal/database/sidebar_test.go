package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSidebarHydrationKeepsParticipantsAndPlaceholderCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidebar.sqlite3")
	d, err := Open(path, 1) // Completing room rows must release the sole reader.
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	d.Now = func() time.Time { return now }
	viewer, err := d.Setup(ctx, "Viewer", "viewer@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	original, err := d.Rooms(ctx, viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 1; i <= 24; i++ {
		id := viewer.ID + int64(i)
		ids = append(ids, id)
		status, role := 0, 0
		if i <= 2 {
			status = i // Inactive/banned participants are retained, not placeholders.
		}
		if i == 3 {
			role = 2 // Active bots remain eligible placeholders.
		}
		if _, err := d.Write.ExecContext(ctx, "INSERT INTO users(id,name,status,role,created_at,updated_at) VALUES(?,?,?,?,?,?)",
			id, fmt.Sprintf("Person %02d", i), status, role, Stamp(now.Add(time.Duration(i)*time.Second)), Stamp(now)); err != nil {
			t.Fatal(err)
		}
	}
	checkPlaceholders := func(want []int64) {
		t.Helper()
		got, err := d.DirectPlaceholders(ctx, viewer.ID)
		if err != nil {
			t.Fatal(err)
		}
		var actual []int64
		for _, participant := range got {
			actual = append(actual, participant.ID)
			full, err := d.User(ctx, participant.ID)
			if err != nil || participant != full.Participant() {
				t.Fatalf("placeholder display data: %+v, %v", participant, err)
			}
		}
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("placeholder IDs: got %v, want %v", actual, want)
		}
	}
	checkPlaceholders(ids[2:21]) // No directs: 19 slots, including the active bot.
	self, err := d.CreateRoom(ctx, viewer.ID, "Rooms::Direct", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkPlaceholders(ids[2:20]) // Distinct viewer plus the appended viewer: 18.
	var directs []Room
	for _, members := range [][]int64{ids[:6], {ids[1], ids[6]}, {ids[7]}, {ids[8]}} {
		room, err := d.CreateRoom(ctx, viewer.ID, "Rooms::Direct", nil, members)
		if err != nil {
			t.Fatal(err)
		}
		directs = append(directs, room)
	}
	if _, err := d.Write.ExecContext(ctx, "UPDATE memberships SET involvement='invisible' WHERE room_id=? AND user_id=?", directs[2].ID, viewer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(ctx, "UPDATE memberships SET involvement=NULL WHERE room_id=? AND user_id=?", directs[3].ID, viewer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(ctx, "UPDATE memberships SET unread_at=? WHERE room_id=? AND user_id=?", Stamp(now), directs[0].ID, viewer.ID); err != nil {
		t.Fatal(err)
	}
	// Hidden and NULL-involvement directs still consume placeholder slots.
	checkPlaceholders(ids[9:18])
	checkRooms := func() []RoomMembership {
		t.Helper()
		rooms, err := d.SidebarRooms(ctx, viewer.ID)
		if err != nil {
			t.Fatal(err)
		}
		var visibleDirects []int64
		for _, room := range rooms {
			if room.Type != "Rooms::Direct" {
				if len(room.Members) != 0 {
					t.Fatal("shared room hydrated direct participants")
				}
				continue
			}
			visibleDirects = append(visibleDirects, room.ID)
			want, err := d.RoomParticipants(ctx, room.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(room.Members, want) {
				t.Fatalf(
					"room %d lost/reordered retained participants: %+v != %+v",
					room.ID,
					room.Members,
					want,
				)
			}
			if room.UpdatedAt != now || room.Unread != (room.ID == directs[0].ID) {
				t.Fatalf("room %d lost activity/unread state", room.ID)
			}
		}
		if !reflect.DeepEqual(visibleDirects, []int64{self.ID, directs[0].ID, directs[1].ID}) {
			t.Fatalf("visible directs changed membership visibility/order: %v", visibleDirects)
		}
		return rooms
	}
	checkRooms()
	// Profiles use the same batched participant preparation but must include
	// hidden and nullable-involvement rooms, unlike the visible sidebar.
	profile, err := d.ProfileRooms(ctx, viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	all := append([]Room{self}, directs...)
	all = append(all, original...)
	if len(profile) != len(all) {
		t.Fatalf("profile lost memberships: %v %v", profile, err)
	}
	for i, room := range profile {
		involvement, err := d.RoomInvolvement(ctx, viewer.ID, room.ID)
		if err != nil || room.Room != all[i] || room.Involvement != involvement.Value {
			t.Fatalf("profile membership changed: %+v %v", room, err)
		}
		if room.Type == "Rooms::Direct" {
			participants, err := d.RoomParticipants(ctx, room.ID)
			if err != nil || !reflect.DeepEqual(room.Members, participants) {
				t.Fatalf("profile participants changed: %+v %v", room, err)
			}
		}
	}
	// Warm prepared queries must observe independently committed SQLite changes.
	external, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	if _, err := external.ExecContext(ctx, "UPDATE users SET name='Changed retained person',updated_at=? WHERE id=?", Stamp(now.Add(time.Hour)), ids[0]); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, room := range checkRooms() {
		for _, member := range room.Members {
			if member.ID == ids[0] {
				found = true
				if member.Name != "Changed retained person" ||
					member.UpdatedAt != now.Add(time.Hour) {
					t.Fatal("batch retained stale participant display data")
				}
			}
		}
	}
	if !found {
		t.Fatal("inactive participant disappeared after external update")
	}
	if _, err := d.CreateRoom(ctx, viewer.ID, "Rooms::Direct", nil, ids[9:17]); err != nil {
		t.Fatal(err)
	}
	checkPlaceholders(ids[17:18]) // Eighteen distinct exclusions leave one slot.
	if _, err := d.CreateRoom(ctx, viewer.ID, "Rooms::Direct", nil, ids[17:18]); err != nil {
		t.Fatal(err)
	}
	checkPlaceholders(nil) // The nineteenth distinct exclusion fills the last slot.
	if _, err := d.CreateRoom(ctx, viewer.ID, "Rooms::Direct", nil, ids); err != nil {
		t.Fatal(err)
	}
	checkPlaceholders(nil) // Existing directs are not capped; placeholders floor at zero.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.SidebarRooms(cancelled, viewer.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("sidebar cancellation: %v", err)
	}
	if _, err := d.RoomParticipants(cancelled, self.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("participant cancellation: %v", err)
	}
	if _, err := d.DirectPlaceholders(cancelled, viewer.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("placeholder cancellation: %v", err)
	}
}
