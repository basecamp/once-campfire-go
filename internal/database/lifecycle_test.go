package database

import (
	"context"
	"database/sql"
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
	if _, err = d.ReachableMessage(ctx, outsider.ID, message.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("private message: %v", err)
	}
	if _, err = d.CreateBoost(ctx, outsider.ID, message.ID, "hidden"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("private boost: %v", err)
	}
	if _, err = d.UpdateMessage(ctx, outsider.ID, message.ID, "hidden", "hidden"); !errors.Is(err, sql.ErrNoRows) {
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
	updated, err := d.UpdateMessage(ctx, owner.ID, message.ID, "edited", "edited")
	if err != nil {
		t.Fatal(err)
	}
	if !updated.UpdatedAt.After(message.UpdatedAt) {
		t.Fatal("edit did not touch message")
	}
	for query, count := range map[string]int{"original": 0, "edited": 1} {
		hits, err := d.Search(ctx, member.ID, query)
		if err != nil || len(hits) != count {
			t.Fatalf("%s: %v %v", query, hits, err)
		}
	}
	boost, err := d.CreateBoost(ctx, member.ID, message.ID, "👍")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.DeleteBoost(ctx, owner.ID, message.ID, boost.ID); !errors.Is(err, sql.ErrNoRows) {
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
	if hits, err := d.Search(ctx, member.ID, "edited"); err != nil || len(hits) != 0 {
		t.Fatal(hits, err)
	}
	if boosts, err := d.Boosts(ctx, message.ID); err != nil || len(boosts) != 0 {
		t.Fatal(boosts, err)
	}
}
func TestRoomConversionAndDeactivation(t *testing.T) {
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
	if err = d.UpdateRoom(ctx, room.ID, "Rooms::Closed", "Private", []int64{owner.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Room(ctx, member.ID, room.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked room access: %v", err)
	}
	if err = d.UpdateRoom(ctx, room.ID, "Rooms::Open", "Shared", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Room(ctx, member.ID, room.ID); err != nil {
		t.Fatal(err)
	}
	direct, err := d.CreateRoom(ctx, owner.ID, "Rooms::Direct", "", []int64{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	same, err := d.CreateRoom(ctx, member.ID, "Rooms::Direct", "", []int64{owner.ID, member.ID})
	if err != nil || same.ID != direct.ID {
		t.Fatal(same, err)
	}
	token, err := d.StartSession(ctx, member.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.DeactivateUser(ctx, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.SessionUser(ctx, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked session: %v", err)
	}
	if _, err = d.Room(ctx, member.ID, room.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deactivated shared membership: %v", err)
	}
	if _, err = d.Room(ctx, member.ID, direct.ID); err != nil {
		t.Fatalf("direct history should remain: %v", err)
	}
}
func TestMessagePaginationAndRefresh(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	u, err := d.Setup(ctx, "User", "user@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var all []Message
	for i := 0; i < 85; i++ {
		now := start.Add(time.Duration(i) * time.Second)
		d.Now = func() time.Time { return now }
		m, err := d.CreateMessage(ctx, u.ID, room.ID, "", "test", "test")
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, m)
	}
	around, err := d.MessagePage(ctx, room.ID, all[42].ID, "around")
	if err != nil || len(around) != 81 || around[0].ID != all[2].ID || around[80].ID != all[82].ID {
		t.Fatalf("around: %d %v", len(around), err)
	}
	d.Now = func() time.Time { return start.Add(100 * time.Second) }
	if _, err = d.UpdateMessage(ctx, u.ID, all[0].ID, "updated", "updated"); err != nil {
		t.Fatal(err)
	}
	created, updated, err := d.RefreshedMessages(ctx, room.ID, start.Add(80*time.Second))
	if err != nil || len(created) != 4 || len(updated) != 1 || updated[0].ID != all[0].ID {
		t.Fatalf("refresh: %d %d %v", len(created), len(updated), err)
	}
}
