package cable

import (
	"net/http"
	"testing"
	"time"
)

func TestCloseJoinsSocketPresenceCleanup(t *testing.T) {
	f := newPresenceFixture(t)
	f.connect(t)
	// Hold the sole writer so cancellation cannot finish the persisted absence.
	tx, err := f.db.Write.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	closed := make(chan struct{})
	go func() { f.hub.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("hub shutdown returned before persisted presence cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-f.ctx.Done():
		t.Fatal("hub did not join disconnected socket", f.ctx.Err())
	}
	if got := f.count(t); got != 0 {
		t.Fatalf("presence still owned after shutdown: %d", got)
	}
	response, err := f.server.Client().Get(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("closed hub admitted another connection: %s", response.Status)
	}
}
