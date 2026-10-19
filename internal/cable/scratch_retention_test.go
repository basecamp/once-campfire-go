package cable

import (
	"context"
	"testing"
)

func TestWarmAuthorizationAvoidsMissScratch(t *testing.T) {
	f := newAuthzFixture(t)
	var recipients []recipient
	for i := 0; i < 8; i++ {
		c, _ := f.join(t, f.stay, f.roomID)
		recipients = append(recipients, recipient{client: c, room: f.roomID})
	}
	f.hub.authorize(context.Background(), recipients, &publishScratch{})
	s := &publishScratch{}
	for i := range recipients {
		recipients[i].user = 0
	}
	f.hub.authorize(context.Background(), recipients, s)
	for _, r := range recipients {
		if r.user != f.stay.ID {
			t.Fatalf("warm authorization: %d", r.user)
		}
	}
	if len(s.authzSeen) != 0 || cap(s.authzMisses) != 0 {
		t.Fatalf("warm hits reserved miss scratch: seen=%d misses capacity=%d", len(s.authzSeen), cap(s.authzMisses))
	}
	// An uncached invalid token does not change the database generation: mix
	// cold duplicate tabs with warm distinct sessions in the same pass.
	missing := recipient{client: &client{token: "missing-session"}, room: f.roomID}
	recipients = append(recipients, missing, missing)
	s.reset()
	f.hub.authorize(context.Background(), recipients, s)
	if len(s.authzMisses) != 1 {
		t.Fatalf("cold tabs not deduplicated: %d", len(s.authzMisses))
	}
	for i, r := range recipients {
		want := f.stay.ID
		if i >= 8 {
			want = 0
		}
		if r.user != want {
			t.Fatalf("mixed recipient %d: got %d want %d", i, r.user, want)
		}
	}
}

func TestPublishScratchReusesBoundedLargeFanout(t *testing.T) {
	s := &publishScratch{authzMisses: make([]authzKey, 5000)}
	s.authzMisses[4999].token = "retained"
	s.reset()
	if cap(s.authzMisses) < 5000 {
		t.Fatal("discarded ordinary large-fanout miss storage")
	}
	for _, key := range s.authzMisses[:cap(s.authzMisses)] {
		if key.token != "" {
			t.Fatal("retained token")
		}
	}
}

func TestPublishScratchClearsCompactedTail(t *testing.T) {
	s := &publishScratch{recips: make([]recipient, 4), tokens: make([]string, 4)}
	s.recips[3].client = &client{}
	s.tokens[3] = "retained session"
	s.recips = s.recips[:1]
	s.tokens = s.tokens[:1]
	s.reset()
	for _, r := range s.recips[:cap(s.recips)] {
		if r.client != nil {
			t.Fatal("scratch retains client in compacted tail")
		}
	}
	for _, token := range s.tokens[:cap(s.tokens)] {
		if token != "" {
			t.Fatal("scratch retains token in compacted tail")
		}
	}
}
