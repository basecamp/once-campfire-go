package presentation

import (
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

func TestAPIMessageCreatorsRemainResponseScoped(t *testing.T) {
	secrets, err := rails.NewSecrets("api-creators-test")
	if err != nil {
		t.Fatal(err)
	}
	r := &Renderer{secrets: secrets}
	stamp := time.Unix(100, 0).UTC()
	content := PrepareAPIContent([]database.Message{
		{ID: 10, CreatorID: 1, Body: "<p>first</p>"},
		{ID: 11, CreatorID: 2, Body: "<p>second</p>"},
		{ID: 12, CreatorID: 1, Body: "<p>third</p>"},
	})
	data := database.MessageAPIData{Authors: map[int64]database.APIAuthor{
		1: {UserDisplay: database.UserDisplay{ID: 1, Name: "Member", UpdatedAt: stamp}},
		2: {UserDisplay: database.UserDisplay{ID: 2, Name: "Admin", UpdatedAt: stamp}, Role: 1},
	}}
	facts := Facts{Host: "first.test", Origin: "https://first.test", Now: stamp}
	first, err := r.APIMessages(facts, content, data)
	if err != nil {
		t.Fatal(err)
	}
	for i, record := range content.Records {
		if want := r.APIUser(facts, data.Authors[record.CreatorID]); first[i].Creator != want {
			t.Fatalf("message %d creator %+v, want %+v", record.ID, first[i].Creator, want)
		}
	}

	// A later response must use its own author metadata and origin, even with
	// the same renderer and message IDs. Earlier returned values stay owned.
	data.Authors[1] = database.APIAuthor{UserDisplay: database.UserDisplay{ID: 1, Name: "Renamed Bot", UpdatedAt: stamp.Add(time.Hour)}, Role: 2}
	facts.Host, facts.Origin = "second.test", "https://second.test"
	second, err := r.APIMessages(facts, content, data)
	if err != nil {
		t.Fatal(err)
	}
	for i, record := range content.Records {
		if want := r.APIUser(facts, data.Authors[record.CreatorID]); second[i].Creator != want {
			t.Fatalf("later message %d creator %+v, want %+v", record.ID, second[i].Creator, want)
		}
	}
	if first[0].Creator.Name != "Member" || first[0].Creator.Role != "member" || first[0].Creator.Avatar == second[0].Creator.Avatar {
		t.Fatal("later preparation changed or reused an earlier creator", first[0].Creator, second[0].Creator)
	}
}
