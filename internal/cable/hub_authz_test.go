// ENGINE-40b authorization cache tests: poisoning (fill, revoke/ban/leave,
// assert the next publication excludes), generation lifetime, and concurrent
// publish/revoke under -race.
package cable

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/coder/websocket"
)

// authzFixture wires a hub with three users (owner from Setup plus two
// created users, all auto-members of the open room). Each test joins its own
// simulated clients to the rooms it publishes to.
type authzFixture struct {
	hub    *Hub
	db     *database.DB
	roomID int64
	stay   database.User // stays authorized in every case
	victim database.User // loses access in every case
	extra  database.User // third member for the removal case
}

func newAuthzFixture(t testing.TB) authzFixture {
	t.Helper()
	hub, db, owner, roomID, _ := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	victim, err := db.CreateUser(ctx, "Victim", "victim@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := db.CreateUser(ctx, "Extra", "extra@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return authzFixture{hub: hub, db: db, roomID: roomID, stay: owner, victim: victim, extra: extra}
}

// join starts a session and a simulated client subscribed to roomID and
// registers it with the hub, returning the client and its token.
func (f *authzFixture) join(t testing.TB, user database.User, roomID int64) (*client, string) {
	t.Helper()
	token, err := f.db.StartSession(context.Background(), user.ID, "authz", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	identifier := `{"channel":"RoomMessagesChannel","room_id":` + strconv.FormatInt(roomID, 10) + `}`
	sub := subscription{Channel: "RoomMessagesChannel", Room: roomID}
	c := simulateClient(t, user, token, identifier, sub)
	f.hub.register(c)
	return c, token
}

// receive blocks until the client's queue produces a frame or the context
// expires (nil then). Use it only where a delivery is guaranteed.
func receive(t testing.TB, ctx context.Context, c *client) *websocket.PreparedMessage {
	t.Helper()
	return testRecv(ctx, c)
}

// expectNone asserts the client's queue is empty right now: nothing is
// pending and (because nothing publishes between checks) nothing will be.
// A revoked client must never have a frame present.
func expectNone(t testing.TB, c *client) {
	t.Helper()
	if f := testGrab(t, c); f != nil {
		t.Fatalf("unexpected delivery of %q", f.Data())
	}
}

// expectDelivery asserts the client either receives exactly the payload
// (want=true, blocking) or must receive nothing (want=false, non-blocking
// empty-queue check).
func expectDelivery(t testing.TB, ctx context.Context, c *client, markup string, want bool) {
	t.Helper()
	if !want {
		if f := testGrab(t, c); f != nil {
			t.Fatalf("unexpected delivery of %q: %q", markup, f.Data())
		}
		return
	}
	f := receive(t, ctx, c)
	if f == nil {
		t.Fatalf("expected a delivery for %q, got none", markup)
	}
	var frame struct{ Message string }
	if err := json.Unmarshal(f.Data(), &frame); err != nil || frame.Message != markup {
		t.Fatalf("wrong frame bytes %q: %v", f.Data(), err)
	}
}

// publishExpect publishes one message and asserts every listed client got
// exactly it (want=true) or nothing (want=false), so every frame every
// publish enqueues is consumed and queues stay aligned.
func publishExpect(t testing.TB, ctx context.Context, f *authzFixture, roomID int64, markup string, clients ...exp) {
	t.Helper()
	f.hub.Publish(ctx, roomID, markup)
	for _, e := range clients {
		expectDelivery(t, ctx, e.c, markup, e.want)
	}
}

type exp struct {
	c    *client
	want bool
}

func got(c *client) exp  { return exp{c, true} }
func none(c *client) exp { return exp{c, false} }

// TestAuthzCachePoisoningBan fills the cache, bans the victim, and requires
// the next publication — and every one after — to exclude them, while the
// stayer keeps receiving. Unban + a fresh session restores access; the old
// token stays excluded.
func TestAuthzCachePoisoningBan(t *testing.T) {
	f := newAuthzFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stayC, _ := f.join(t, f.stay, f.roomID)
	victimC, _ := f.join(t, f.victim, f.roomID)

	publishExpect(t, ctx, &f, f.roomID, `<turbo-stream action="append"><template>fill</template></turbo-stream>`, got(stayC), got(victimC))

	if err := f.db.BanUser(context.Background(), f.victim.ID, true); err != nil {
		t.Fatal(err)
	}
	after := "<turbo-stream action=\"append\"><template>after-ban</template></turbo-stream>"
	publishExpect(t, ctx, &f, f.roomID, after, none(victimC), got(stayC)) // never served from cache
	later := "<turbo-stream action=\"append\"><template>later</template></turbo-stream>"
	publishExpect(t, ctx, &f, f.roomID, later, none(victimC), got(stayC))

	// Unban + re-login: a fresh token authorizes again; the old token's
	// negative cache entry can never outlive the new session's entry, and
	// the old token itself still has no session row.
	if err := f.db.BanUser(context.Background(), f.victim.ID, false); err != nil {
		t.Fatal(err)
	}
	restoredC, _ := f.join(t, f.victim, f.roomID)
	restored := "<turbo-stream action=\"append\"><template>restored</template></turbo-stream>"
	publishExpect(t, ctx, &f, f.roomID, restored, got(restoredC), none(victimC), got(stayC))
}

// TestAuthzCachePoisoningLogout fills the cache, deletes the victim's
// session (logout), and requires exclusion from the next publication.
func TestAuthzCachePoisoningLogout(t *testing.T) {
	f := newAuthzFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stayC, _ := f.join(t, f.stay, f.roomID)
	victimC, victimTok := f.join(t, f.victim, f.roomID)
	publishExpect(t, ctx, &f, f.roomID, `<turbo-stream action="append"><template>fill</template></turbo-stream>`, got(stayC), got(victimC))

	if err := f.db.DeleteSession(context.Background(), victimTok, f.victim.ID); err != nil {
		t.Fatal(err)
	}
	after := "<turbo-stream action=\"append\"><template>after-logout</template></turbo-stream>"
	publishExpect(t, ctx, &f, f.roomID, after, none(victimC), got(stayC))
	later := "<turbo-stream action=\"append\"><template>later</template></turbo-stream>"
	publishExpect(t, ctx, &f, f.roomID, later, none(victimC), got(stayC))

	// The revocation must also stick when the session was already gone.
	if err := f.db.DeleteSession(context.Background(), victimTok, f.victim.ID); err != nil {
		t.Fatal(err)
	}
	publishExpect(t, ctx, &f, f.roomID, after, none(victimC), got(stayC))
}

// TestAuthzCachePoisoningMembershipRemoval fills the cache for a closed room
// with three members, removes the victim's membership, and requires
// exclusion while both remaining members keep receiving.
func TestAuthzCachePoisoningMembershipRemoval(t *testing.T) {
	f := newAuthzFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	closed, err := f.db.CreateRoom(ctx, f.stay.ID, "Rooms::Closed", "Quiet", []int64{f.stay.ID, f.victim.ID, f.extra.ID})
	if err != nil {
		t.Fatal(err)
	}
	stayC, _ := f.join(t, f.stay, closed.ID)
	victimC, _ := f.join(t, f.victim, closed.ID)
	extraC, _ := f.join(t, f.extra, closed.ID)
	publishExpect(t, ctx, &f, closed.ID, `<turbo-stream action="append"><template>fill</template></turbo-stream>`, got(stayC), got(victimC), got(extraC))

	if err := f.db.UpdateRoom(ctx, closed.ID, "Rooms::Closed", "Quiet", []int64{f.stay.ID, f.extra.ID}); err != nil {
		t.Fatal(err)
	}
	after := "<turbo-stream action=\"append\"><template>after-removal</template></turbo-stream>"
	publishExpect(t, ctx, &f, closed.ID, after, none(victimC), got(stayC), got(extraC))
	later := "<turbo-stream action=\"append\"><template>later</template></turbo-stream>"
	publishExpect(t, ctx, &f, closed.ID, later, none(victimC), got(stayC), got(extraC))
}

// TestAuthzEntryNeverOutlivesGenerations pins the lifetime contract directly:
// an entry stores the generations its query ran under, any session write
// moves SessionVersion so the entry misses, and the next publication
// re-queries and re-stores under the current generations.
func TestAuthzEntryNeverOutlivesGenerations(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()
	stayC, _ := f.join(t, f.stay, f.roomID)
	victimC, victimTok := f.join(t, f.victim, f.roomID)
	publishExpect(t, ctx, &f, f.roomID, "<turbo-stream action=\"append\"><template>fill</template></turbo-stream>", got(stayC), got(victimC))
	key := authzKey{room: f.roomID, token: victimTok}

	f.hub.authz.mu.Lock()
	e := f.hub.authz.m[key]
	f.hub.authz.mu.Unlock()
	if e == nil {
		t.Fatal("no cache entry after the first publish")
	}
	if e.userID != f.victim.ID {
		t.Fatalf("entry user %d, want %d", e.userID, f.victim.ID)
	}
	if e.sess != f.db.SessionVersion() || e.member != f.db.MembershipVersion() || e.userVersion != f.db.UserVersion() {
		t.Fatal("entry stored generations that differ from the current counters")
	}

	// Any session write moves SessionVersion; the stored entry must now
	// miss, so it is stale relative to the counters.
	if _, err := f.db.StartSession(ctx, f.stay.ID, "authz", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	f.hub.authz.mu.Lock()
	stale := e.sess == f.db.SessionVersion()
	f.hub.authz.mu.Unlock()
	if stale {
		t.Fatal("entry generations did not go stale after a session write")
	}

	// The next publication misses, re-queries, and refreshes the entry in
	// place with the current generations and the current answer.
	markup := "<turbo-stream action=\"append\"><template>refreshed</template></turbo-stream>"
	publishExpect(t, ctx, &f, f.roomID, markup, got(victimC), got(stayC))
	f.hub.authz.mu.Lock()
	fresh := e.sess == f.db.SessionVersion() && e.member == f.db.MembershipVersion() && e.userVersion == f.db.UserVersion() && e.userID == f.victim.ID
	f.hub.authz.mu.Unlock()
	if !fresh {
		t.Fatal("entry was not refreshed with the current generations and the current answer")
	}
}

// TestConcurrentPublishEveryRecipient publishes concurrently from several
// goroutines and requires every client to receive exactly every payload
// once. The steady state exercises the shared authorization cache (hits) and
// the frame cache from many goroutines; the suite runs it under -race.
func TestConcurrentPublishEveryRecipient(t *testing.T) {
	hub, db, user, roomID, _ := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const clientsN, publishers, perPublisher = 64, 4, 25
	identifier := `{"channel":"RoomMessagesChannel","room_id":` + strconv.FormatInt(roomID, 10) + `}`
	sub := subscription{Channel: "RoomMessagesChannel", Room: roomID}
	var clients []*client
	for i := 0; i < clientsN; i++ {
		token, err := db.StartSession(ctx, user.ID, "concurrent", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		c := simulateClient(t, user, token, identifier, sub)
		clients = append(clients, c)
		hub.register(c)
	}
	payloads := make([]string, publishers*perPublisher)
	var wg sync.WaitGroup
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perPublisher; i++ {
				markup := fmt.Sprintf("<turbo-stream action=\"append\"><template>%d-%d</template></turbo-stream>", p, i)
				payloads[p*perPublisher+i] = markup
				hub.Publish(ctx, roomID, markup)
			}
		}(p)
	}
	wg.Wait()
	for _, c := range clients {
		seen := make(map[string]bool, len(payloads))
		for range payloads {
			f := receive(t, ctx, c)
			if f == nil {
				t.Fatalf("client %p missing deliveries: got %d of %d (queue len %d)", c, len(seen), len(payloads), testQueueLen(c))
			}
			var frame struct{ Message string }
			if err := json.Unmarshal(f.Data(), &frame); err != nil {
				t.Fatalf("client %p undecodable frame %q: %v", c, f.Data(), err)
			}
			if seen[frame.Message] {
				t.Fatalf("client %p received duplicate payload %q", c, frame.Message)
			}
			seen[frame.Message] = true
		}
		if len(seen) != len(payloads) {
			t.Fatalf("client %p received %d distinct payloads of %d", c, len(seen), len(payloads))
		}
		expectNone(t, c)
	}
}

// stormMember is one member of the revocation-storm room.
type stormMember struct {
	client *client
	user   database.User
}

func stormIDs(members []stormMember) []int64 {
	ids := make([]int64, len(members))
	for i, m := range members {
		ids[i] = m.user.ID
	}
	return ids
}

func stormClients(members []stormMember) []*client {
	clients := make([]*client, len(members))
	for i, m := range members {
		clients[i] = m.client
	}
	return clients
}

func drainAll(clients []*client) {
	for _, c := range clients {
		c.q.clear()
	}
}

// TestConcurrentPublishWithRevocation runs concurrent publishes against a
// room whose membership a revoker goroutine is shrinking through the audited
// helper. Every delivered frame must be one of the published payloads, and a
// final publish after the storm must reach no one (every member was
// revoked). Exists for the -race run over the shared authorization cache
// under write traffic; ordering semantics are pinned by the poisoning tests.
func TestConcurrentPublishWithRevocation(t *testing.T) {
	hub, db, user, _, _ := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	closed, err := db.CreateRoom(ctx, user.ID, "Rooms::Closed", "Storm", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	const membersN, publishers, perPublisher = 16, 2, 30
	identifier := `{"channel":"RoomMessagesChannel","room_id":` + strconv.FormatInt(closed.ID, 10) + `}`
	sub := subscription{Channel: "RoomMessagesChannel", Room: closed.ID}
	members := make([]stormMember, 0, membersN)
	for i := 0; i < membersN; i++ {
		m, err := db.CreateUser(ctx, fmt.Sprintf("Member%d", i), fmt.Sprintf("m%d@test", i), "digest", "", 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.UpdateRoom(ctx, closed.ID, "Rooms::Closed", "Storm", append(stormIDs(members), m.ID)); err != nil {
			t.Fatal(err)
		}
		token, err := db.StartSession(ctx, m.ID, "storm", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		c := simulateClient(t, m, token, identifier, sub)
		members = append(members, stormMember{c, m})
		hub.register(c)
	}
	total := publishers * perPublisher
	payloads := make(map[string]bool, total)
	for p := 0; p < publishers; p++ {
		for i := 0; i < perPublisher; i++ {
			payloads[fmt.Sprintf("<turbo-stream action=\"append\"><template>%d-%d</template></turbo-stream>", p, i)] = true
		}
	}
	// Drainers keep every queue open and validate every frame delivered
	// during the storm, including frames sent before a revocation commit.
	var drainWG sync.WaitGroup
	stormOver := make(chan struct{})
	for _, m := range members {
		drainWG.Add(1)
		go func(c *client) {
			defer drainWG.Done()
			for {
				f := testRecvNow(c)
				if f == nil {
					select {
					case <-stormOver:
						return
					case <-time.After(time.Millisecond):
						continue
					}
				}
				var frame struct{ Message string }
				if err := json.Unmarshal(f.Data(), &frame); err != nil || !payloads[frame.Message] {
					t.Errorf("client %p received non-publication frame %q", c, f.Data())
				}
			}
		}(m.client)
	}
	var wg sync.WaitGroup
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perPublisher; i++ {
				hub.Publish(ctx, closed.ID, fmt.Sprintf("<turbo-stream action=\"append\"><template>%d-%d</template></turbo-stream>", p, i))
			}
		}(p)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Revoke members one at a time through the audited helper, ending
		// with zero memberships in the room.
		for i := 0; i < membersN; i++ {
			kept := stormIDs(members[i+1:])
			if err := db.UpdateRoom(ctx, closed.ID, "Rooms::Closed", "Storm", kept); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
	close(stormOver)
	drainWG.Wait()
	// The queues are now quiescent (every publisher and revoker finished,
	// every drainer saw the storm frames): the final publication must reach
	// nobody, because no membership survived.
	drainAll(stormClients(members))
	final := "<turbo-stream action=\"append\"><template>final</template></turbo-stream>"
	hub.Publish(ctx, closed.ID, final)
	for _, m := range members {
		expectNone(t, m.client)
	}
}
