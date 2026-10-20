package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestMessageAPIReadKeepsRecordsMetadataAndRolesTogether(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "API author", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0].ID
	message, err := d.CreateMessage(ctx, owner.ID, room, messageInput("", "before"))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := d.CreateBlob(ctx, Blob{Filename: "before.txt", ByteSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.UpdateMessage(ctx, owner.ID, message.ID, MessageInput{Attachment: &blob.ID}); err != nil {
		t.Fatal(err)
	}
	read, err := d.BeginMessageRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	records, count, next, err := read.APIPage(ctx, owner.ID, room, 0, "before", false)
	if err != nil || len(records) != 1 || records[0].Body != "before" || count != 1 || next != 0 {
		t.Fatalf("API selection: %+v %d %d %v", records, count, next, err)
	}
	if _, err = d.UpdateMessage(ctx, owner.ID, message.ID, messageInput("", "after")); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Write.ExecContext(ctx, "UPDATE users SET role=2,name='after' WHERE id=?", owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Write.ExecContext(ctx, "UPDATE active_storage_blobs SET filename='after.txt' WHERE id=?", blob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Write.ExecContext(ctx, "DELETE FROM memberships WHERE user_id=? AND room_id=?", owner.ID, room); err != nil {
		t.Fatal(err)
	}
	data, err := read.APIData(ctx, records, []int64{owner.ID})
	if err != nil || data.Authors[owner.ID].Name != "API author" || data.Authors[owner.ID].Role != 1 || data.Mentions[owner.ID].Name != "API author" || data.Attachments[message.ID] != "before.txt" {
		t.Fatalf("API associations escaped selection: %+v %v", data, err)
	}
	if err = read.Finish(); err != nil {
		t.Fatal(err)
	}
	read, err = d.BeginMessageRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if _, _, _, err = read.APIPage(ctx, owner.ID, room, 0, "before", false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("new API observation retained revoked scope: %v", err)
	}
}
