package database

import (
	"context"
	"reflect"
	"testing"
)

func TestSidebarSnapshotMatchesSeparateQueries(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	member, err := d.CreateUser(ctx, "Member", "member@test", "digest", "bio", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.CreateUser(ctx, "Placeholder", "placeholder@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := d.CreateRoom(ctx, owner.ID, "Rooms::Direct", "", []int64{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Scramble membership row IDs to expose differences between index orders.
	if _, err := d.Write.ExecContext(ctx, "UPDATE memberships SET id=100000-id WHERE room_id=?", direct.ID); err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []bool{false, true} {
		if hidden {
			if _, err := d.Write.ExecContext(ctx, "UPDATE memberships SET involvement='invisible' WHERE room_id=? AND user_id=?", direct.ID, owner.ID); err != nil {
				t.Fatal(err)
			}
		}
		rooms, members, placeholders, err := d.Sidebar(ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		expectedRooms, err := d.SidebarRooms(ctx, owner.ID)
		if err != nil || !reflect.DeepEqual(rooms, expectedRooms) {
			t.Fatal("room list differs", rooms, expectedRooms, err)
		}
		expectedPlaceholders, err := d.DirectPlaceholders(ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(placeholders) != len(expectedPlaceholders) {
			t.Fatal("placeholder count differs")
		}
		for i, u := range placeholders {
			want := expectedPlaceholders[i]
			if u.ID != want.ID || u.Name != want.Name || !u.UpdatedAt.Equal(want.UpdatedAt) {
				t.Fatal("placeholder differs", u, want)
			}
		}
		for _, room := range rooms {
			if room.Type != "Rooms::Direct" {
				continue
			}
			want, err := d.RoomMembers(ctx, room.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(members[room.ID]) != len(want) {
				t.Fatal("member count differs")
			}
			for i, u := range members[room.ID] {
				if u.ID != want[i].ID || u.Name != want[i].Name || !u.UpdatedAt.Equal(want[i].UpdatedAt) {
					t.Fatal("member differs", u, want[i])
				}
			}
		}
	}
}
