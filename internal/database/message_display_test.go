package database

import (
	"context"
	"testing"
	"time"
)

func TestMessageDisplaysKeepSeparateCollectionsAndLowestAttachment(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateUser(ctx, owner.ID, UserInput{Name: "Other", Email: "other@test", Password: "digest", Bio: "Target"})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := d.CreateRoom(ctx, owner.ID, "Rooms::Direct", nil, []int64{owner.ID, other.ID})
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := "body"
	first, err := d.CreateMessage(ctx, owner.ID, rooms[0].ID, MessageInput{Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.CreateMessage(ctx, other.ID, direct.ID, MessageInput{Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"first boost", "second boost"} {
		if _, err = d.CreateBoost(ctx, owner.ID, first.ID, content); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = d.CreateBoost(ctx, other.ID, second.ID, "other boost"); err != nil {
		t.Fatal(err)
	}
	blob, err := d.CreateBlob(ctx, Blob{Filename: "lowest", ByteSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	next, err := d.CreateBlob(ctx, Blob{Filename: "later", ByteSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	// Existing installations can have multiple attachment edges. The lowest
	// attachment identity, not blob identity or bytes, selects the display.
	for _, id := range []int64{blob.ID, next.ID} {
		if _, err = d.Write.ExecContext(ctx, "INSERT INTO active_storage_attachments(name,record_type,record_id,blob_id,created_at) VALUES ('attachment','Message',?,?,?)", first.ID, id, Stamp(d.Now())); err != nil {
			t.Fatal(err)
		}
	}
	data, users, err := d.MessageDisplays(ctx, []Message{first.Message, second.Message}, []int64{other.ID, 99999})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 2 || len(users) != 1 || users[other.ID].Title() != "Other – Target" {
		t.Fatal(data, users)
	}
	if data[first.ID].Author == nil || data[first.ID].Author.ID != owner.ID || data[second.ID].Author == nil || data[second.ID].Author.ID != other.ID {
		t.Fatal("mention selection lost message authors", data)
	}
	_, users, err = d.MessageDisplays(ctx, []Message{first.Message}, nil)
	if err != nil || len(users) != 0 {
		t.Fatal("unmentioned authors leaked into mention projection", users, err)
	}
	_, users, err = d.MessageDisplays(ctx, []Message{first.Message}, []int64{owner.ID})
	if err != nil || len(users) != 1 || users[owner.ID].Name != owner.Name {
		t.Fatal("mentioned author missing", users, err)
	}
	if data[first.ID].Attachment == nil || data[first.ID].Attachment.ID != blob.ID || data[second.ID].Attachment != nil {
		t.Fatal("attachment collections mixed", data)
	}
	if len(data[first.ID].Boosts) != 2 || data[first.ID].Boosts[0].Content != "first boost" || len(data[second.ID].Boosts) != 1 || data[second.ID].Boosts[0].Content != "other boost" {
		t.Fatal("boost collections multiplied or mixed", data)
	}
	if len(data[second.ID].Participants) != 2 || data[second.ID].Participants[0].ID != owner.ID || data[second.ID].Participants[1].ID != other.ID {
		t.Fatal("direct participants lost", data[second.ID])
	}
	// No snapshot may survive preparation: all reader slots remain available.
	d.Read.SetMaxOpenConns(1)
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err = d.User(deadline, owner.ID); err != nil {
		t.Fatal(err)
	}
}
