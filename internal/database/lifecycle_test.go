package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMessageLifecyclePermissionsAndSearch(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	member, err := d.CreateUser(ctx, "Member", "member@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := d.CreateUser(ctx, "Outsider", "outsider@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	room, err := d.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Private", []int64{owner.ID, member.ID})
	if err != nil {
		t.Fatal(err)
	}
	message, err := d.CreateMessage(ctx, member.ID, room.ID, "", "original", "original")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.ReachableMessage(ctx, outsider.ID, message.ID); !errors.Is(err, ErrNoRows) {
		t.Fatalf("private message: %v", err)
	}
	if _, err = d.CreateBoost(ctx, outsider.ID, message.ID, "hidden"); !errors.Is(err, ErrNoRows) {
		t.Fatalf("private boost: %v", err)
	}
	hidden := "hidden"
	if _, err = d.UpdateMessageWithUpload(ctx, outsider.ID, message.ID, &hidden, hidden, nil, nil); !errors.Is(err, ErrNoRows) {
		t.Fatalf("private edit: %v", err)
	}
	other, err := d.CreateMessage(ctx, owner.ID, room.ID, "", "admin message", "admin message")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.DeleteMessage(ctx, member.ID, other.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delete another user's message: %v", err)
	}
	d.Now = func() time.Time { return message.CreatedAt.Add(time.Minute) }
	edited := "edited"
	updated, err := d.UpdateMessageWithUpload(ctx, owner.ID, message.ID, &edited, edited, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.UpdatedAt.After(message.UpdatedAt) {
		t.Fatal("edit did not touch message")
	}
	for query, count := range map[string]int{"original": 0, "edited": 1} {
		hits, err := d.MessageSearchReachable(ctx, member.ID, query)
		if err != nil || len(hits) != count {
			t.Fatalf("%s: %v %v", query, hits, err)
		}
	}
	boost, err := d.CreateBoost(ctx, member.ID, message.ID, "👍")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.DeleteBoost(ctx, owner.ID, message.ID, boost.ID); !errors.Is(err, ErrNoRows) {
		t.Fatalf("admin cannot delete someone else's boost: %v", err)
	}
	if err = d.DeleteBoost(ctx, member.ID, message.ID, boost.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.CreateBoost(ctx, owner.ID, message.ID, "again"); err != nil {
		t.Fatal(err)
	}
	if err = d.DeleteMessage(ctx, member.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	if hits, err := d.MessageSearchReachable(ctx, member.ID, "edited"); err != nil || len(hits) != 0 {
		t.Fatal(hits, err)
	}
	var boosts int
	if err = d.Read.QueryRowContext(ctx, "SELECT count(*) FROM boosts WHERE message_id=?", message.ID).Scan(&boosts); err != nil || boosts != 0 {
		t.Fatal(boosts, err)
	}
}
func TestDeactivation(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	member, err := d.CreateUser(ctx, "Member", "member@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	room, err := d.CreateRoom(ctx, owner.ID, "Rooms::Open", "Shared", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Room(ctx, member.ID, room.ID); err != nil {
		t.Fatal(err)
	}
	direct, err := d.CreateRoom(ctx, owner.ID, "Rooms::Direct", "", []int64{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	token, err := d.StartSession(ctx, member.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.DeactivateUser(ctx, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.SessionUser(ctx, token); !errors.Is(err, ErrNoRows) {
		t.Fatalf("revoked session: %v", err)
	}
	if _, err = d.Room(ctx, member.ID, room.ID); !errors.Is(err, ErrNoRows) {
		t.Fatalf("deactivated shared membership: %v", err)
	}
	if _, err = d.Room(ctx, member.ID, direct.ID); err != nil {
		t.Fatalf("direct history should remain: %v", err)
	}
}
