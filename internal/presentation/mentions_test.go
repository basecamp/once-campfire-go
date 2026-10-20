package presentation

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
)

func TestMentionContextCapturesFactsForConcurrentResolution(t *testing.T) {
	secrets, err := rails.NewSecrets("mention-context-test")
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(100, 0).UTC()
	users := map[int64]database.UserDisplay{
		7: {ID: 7, Name: "First", Bio: "original bio", UpdatedAt: stamp},
		9: {ID: 9, Name: "Second", UpdatedAt: stamp},
	}
	wants := []richtext.Mention{Mention(secrets, users[7]), Mention(secrets, users[9])}
	targets := map[string]MentionTarget{}
	for _, want := range wants {
		targets[want.SGID] = MentionTargetFor(want.SGID)
	}
	const bad = "!not-base64"
	targets[bad] = MentionTargetFor(bad)
	tampered := wants[0].SGID[:len(wants[0].SGID)-1] + "!"
	targets[tampered] = MentionTargetFor(tampered)
	ctx := MentionContext(secrets, "campfire.test:3000", stamp, targets, users)
	users[7] = database.UserDisplay{ID: 7, Name: "Changed", UpdatedAt: stamp.Add(time.Hour)}
	delete(users, 9)

	var work sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		work.Go(func() {
			<-start
			for _, want := range wants {
				for _, verified := range []bool{false, true} {
					got, err := ctx.Resolve(want.SGID, verified)
					if err != nil || got == nil || *got != want {
						t.Errorf("captured mention: got %+v (%v), want %+v", got, err, want)
					}
				}
			}
		})
	}
	close(start)
	work.Wait()
	// Reusing a prepared user must not reuse a different token's verification.
	if got, err := ctx.Resolve(tampered, false); err != nil || got == nil || *got != wants[0] {
		t.Fatal("lost the display-only User exception", got, err)
	}
	if got, err := ctx.Resolve(tampered, true); got != nil || err != nil {
		t.Fatal("reused verification for a tampered token", got, err)
	}
	if got, err := ctx.Resolve(bad, false); got != nil || err == nil || err != targets[bad].Error {
		t.Fatal("lost malformed-target error", got, err)
	}
	if got, err := ctx.Resolve(bad, true); got != nil || err != nil {
		t.Fatal("verified invalid recipient changed", got, err)
	}
	fresh := MentionContext(secrets, "campfire.test", stamp, targets, users)
	if got, err := fresh.Resolve(wants[0].SGID, false); err != nil || got == nil || *got != Mention(secrets, users[7]) {
		t.Fatal("new context did not observe new facts", got, err)
	}
}

func TestVerifiedMentionsPreserveSupportedSGIDs(t *testing.T) {
	raw, err := os.ReadFile("../../reference/vectors/rails_compat.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Secret string `json:"secret_key_base"`
		SGIDs  struct {
			Verify []struct {
				Case, SGID, Purpose, Now string
				Expected                 *string
			}
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	secrets, err := rails.NewSecrets(fixture.Secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range fixture.SGIDs.Verify {
		if test.Purpose != "attachable" {
			continue
		}
		t.Run(test.Case, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339Nano, test.Now)
			if err != nil {
				t.Fatal(err)
			}
			var want int64
			if test.Expected != nil {
				want = rails.UserGIDID(*test.Expected)
			}
			id := VerifiedMentionID(secrets, test.SGID, now)
			if id != want {
				t.Fatalf("prepared recipient %d, want %d", id, want)
			}
			users := map[int64]database.UserDisplay{}
			if want != 0 {
				users[want] = database.UserDisplay{ID: want, Name: "User"}
			}
			ctx := MentionContext(secrets, "campfire.test", now, nil, users)
			mention, err := ctx.Resolve(test.SGID, true)
			if err != nil {
				t.Fatal(err)
			}
			if want == 0 {
				if mention != nil {
					t.Fatal("accepted invalid recipient", mention)
				}
			} else if mention == nil || mention.ID != want {
				t.Fatal("lost supported recipient", mention)
			}
		})
	}
}
