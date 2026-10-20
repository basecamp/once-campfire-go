package database

import (
	"context"
	"testing"
	"time"
)

func TestBoostCommitsOwnCurrentTargetsWithoutReader(t *testing.T) {
	d := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	actor, err := d.Setup(ctx, "Before", "boost@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := d.CreateMessage(ctx, actor.ID, rooms[0].ID, messageInput("before", "message"))
	if err != nil {
		t.Fatal(err)
	}
	// Earlier HTTP observations are not authoritative publication inputs.
	if _, err = d.Write.ExecContext(ctx, "UPDATE messages SET client_message_id='current' WHERE id=?", message.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Write.ExecContext(ctx, "UPDATE users SET name='Current',bio='Writer display' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	for range d.Read.Stats().MaxOpenConnections {
		conn, err := d.Read.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	created, err := d.CreateBoost(ctx, actor.ID, message.ID, "yes")
	if err != nil {
		t.Fatal(err)
	}
	if created.RoomID != message.RoomID || created.ClientID != "current" || created.MessageID != message.ID || created.BoosterID != actor.ID || created.Booster != "Current" || created.BoosterTitle != "Current – Writer display" || created.Content != "yes" || created.ID == 0 {
		t.Fatalf("writer receipt: %+v", created)
	}
	removed, err := d.DeleteBoost(ctx, actor.ID, message.ID, created.ID)
	if err != nil || removed.ID != created.ID || removed.RoomID != message.RoomID || removed.MessageID != message.ID {
		t.Fatalf("removal receipt: %+v %v", removed, err)
	}
	var count int
	if err = d.Write.QueryRowContext(ctx, "SELECT count(*) FROM boosts WHERE id=?", created.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("boost remains", count, err)
	}
}
