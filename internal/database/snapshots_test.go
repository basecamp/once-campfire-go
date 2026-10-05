package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotsObserveExternalChangesAndDoNotShareMutableRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite3")
	d, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	external, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := external.Close(); err != nil {
			t.Error(err)
		}
	})
	owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	account, err := d.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		account, err = d.Account(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	account.Settings[0] = '!'
	account, err = d.Account(ctx)
	if err != nil || account.Settings[0] == '!' {
		t.Fatal("mutable cached account settings", account, err)
	}
	if _, err := external.Exec(`UPDATE accounts SET name='Renamed', settings='{"restrict_room_creation_to_administrators":true}' WHERE id=?`, account.ID); err != nil {
		t.Fatal(err)
	}
	account, err = d.Account(ctx)
	if err != nil || account.Name != "Renamed" || !account.RestrictRooms() {
		t.Fatal("external account edit was missed", account, err)
	}
	member, err := d.CreateUser(ctx, "Member", "member@test", "digest", "bio", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	room, err := d.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Private", []int64{owner.ID, member.ID})
	if err != nil {
		t.Fatal(err)
	}
	message, err := d.CreateMessage(ctx, member.ID, room.ID, "id", "coffee", "coffee")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := d.MessagePageReferences(ctx, room.ID, 0, "before")
	if err != nil || len(refs) != 1 || refs[0].ID != message.ID {
		t.Fatal(refs, err)
	}
	for range 8 {
		refs, err = d.MessagePageReferences(ctx, room.ID, 0, "before")
		if err != nil {
			t.Fatal(err)
		}
	}
	refs[0].ID = 999
	refs, err = d.MessagePageReferences(ctx, room.ID, 0, "before")
	if err != nil || refs[0].ID != message.ID {
		t.Fatal("mutable cached reference", refs, err)
	}
	stamp := message.UpdatedAt.Add(time.Hour).Truncate(time.Microsecond)
	if _, err := external.Exec("UPDATE messages SET updated_at=? WHERE id=?", Stamp(stamp), message.ID); err != nil {
		t.Fatal(err)
	}
	refs, err = d.MessagePageReferences(ctx, room.ID, 0, "before")
	if err != nil || !refs[0].UpdatedAt.Equal(stamp) {
		t.Fatal("external message edit was missed", refs, err)
	}
	members, err := d.RoomMembers(ctx, room.ID)
	if err != nil || len(members) != 2 {
		t.Fatal(members, err)
	}
	members[0].Name = "mutated"
	members, err = d.RoomMembers(ctx, room.ID)
	if err != nil || members[0].Name == "mutated" {
		t.Fatal("mutable cached user", members, err)
	}
	if _, err := external.Exec("UPDATE users SET name=? WHERE id=?", "Changed <member>", member.ID); err != nil {
		t.Fatal(err)
	}
	members, err = d.RoomMembers(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range members {
		if u.ID == member.ID {
			found = u.Name == "Changed <member>"
		}
	}
	if !found {
		t.Fatal("external user edit was missed", members)
	}
	hits, err := d.Search(ctx, member.ID, "coffee")
	if err != nil || len(hits) != 1 {
		t.Fatal(hits, err)
	}
	sidebar, err := d.SidebarRooms(ctx, member.ID)
	if err != nil || len(sidebar) < 1 {
		t.Fatal(sidebar, err)
	}
	if _, _, _, err := d.Sidebar(ctx, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := external.Exec("DELETE FROM memberships WHERE user_id=? AND room_id=?", member.ID, room.ID); err != nil {
		t.Fatal(err)
	}
	// Visit every observer, including ones that haven't seen this commit yet.
	for range 16 {
		hits, err = d.Search(ctx, member.ID, "coffee")
		if err != nil || len(hits) != 0 {
			t.Fatal("revoked search access leaked", hits, err)
		}
	}
	sidebar, err = d.SidebarRooms(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range sidebar {
		if r.ID == room.ID {
			t.Fatal("revoked sidebar membership leaked")
		}
	}
	currentSidebar, _, _, err := d.Sidebar(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range currentSidebar {
		if r.ID == room.ID {
			t.Fatal("revoked combined sidebar membership leaked")
		}
	}
	if _, err := external.Exec("DELETE FROM messages WHERE id=?", message.ID); err != nil {
		t.Fatal(err)
	}
	refs, err = d.MessagePageReferences(ctx, room.ID, 0, "before")
	if err != nil || len(refs) != 0 {
		t.Fatal("deleted message returned", refs, err)
	}
}
