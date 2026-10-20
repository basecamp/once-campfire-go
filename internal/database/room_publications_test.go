package database

import (
	"context"
	"reflect"
	"testing"
)

func TestRoomAudienceRetainsInactivePingNamesButNotRecipients(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "Zulu", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	active, err := d.CreateUser(ctx, owner.ID, UserInput{Name: "Alpha", Email: "active@test", Password: "digest"})
	if err != nil {
		t.Fatal(err)
	}
	inactive, err := d.CreateUser(ctx, owner.ID, UserInput{Name: "Beta", Email: "inactive@test", Password: "digest"})
	if err != nil {
		t.Fatal(err)
	}
	room, err := d.CreateRoom(ctx, owner.ID, "Rooms::Direct", nil, []int64{active.ID, inactive.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.DeactivateUser(ctx, owner.ID, inactive.ID); err != nil {
		t.Fatal(err)
	}
	audience, err := d.RoomPublicationAudience(ctx, room)
	if err != nil || !reflect.DeepEqual(audience.Active, []int64{active.ID, owner.ID}) {
		t.Fatalf("active name-ordered recipients: %+v %v", audience, err)
	}
	want := []RoomParticipant{owner.Participant(), active.Participant(), inactive.Participant()}
	// Deactivation changes the user's timestamp, not their retained identity/name.
	if len(audience.Participants) != len(want) {
		t.Fatal("retained participants lost", audience)
	}
	for i, member := range audience.Participants {
		if member.ID != want[i].ID || member.Name != want[i].Name {
			t.Fatalf("ID-ordered retained participant: %+v instead of %+v", member, want[i])
		}
	}
}
