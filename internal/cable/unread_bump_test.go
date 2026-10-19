// Lever 5 tests: the unread-bump publication that intersects the hub's ACTIVE
// unread subscriptions with the room's members. Delivery is pinned across the
// two dimensions the old fan-out scaled with — room members (which must no
// longer cost per-member publishes) and active subscribers — together with the
// candidate strategy's scaling (unrelated subscribers cost nothing), the
// zero-subscriber shortcut's statement/allocation budget, the two eligibility
// rechecks (membership of the stream's target user, session validity of the
// client) and the snapshot boundary that makes the shortcut safe.
package cable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
)

// unreadIdentifier is the subscription identifier an UnreadRoomsChannel
// client uses (the rooms-list Stimulus controller subscribes with the channel
// name alone, so every client's identifier is this one string).
const unreadIdentifier = `{"channel":"UnreadRoomsChannel"}`

// unreadBudget is the budget the tests hand the bump: generous, so only the
// deadline behaviour under test can fail a call.
const unreadBudget = 5 * time.Second

// unreadSubscription is the stored subscription for a client's own unread
// stream, exactly what Hub.subscription derives for UnreadRoomsChannel.
func unreadSubscription(user database.User) subscription {
	return subscription{Channel: "UnreadRoomsChannel", Stream: unreadStreamName(user.ID)}
}

// unreadSubscriber starts a session for user and registers a simulated client
// subscribed to that user's unread stream, returning the client and its token.
func unreadSubscriber(t testing.TB, hub *Hub, db *database.DB, user database.User) (*client, string) {
	t.Helper()
	token, err := db.StartSession(context.Background(), user.ID, "unreads", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c := simulateClient(t, user, token, unreadIdentifier, unreadSubscription(user))
	hub.register(c)
	return c, token
}

// signedAliasIdentifier is the Turbo::StreamsChannel identifier for a
// server-signed stream name — the second way the subscription API accepts a
// subscription to the unread stream.
func signedAliasIdentifier(t testing.TB, secrets *rails.Secrets, stream string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"channel": "Turbo::StreamsChannel", "signed_stream_name": secrets.SignStream(stream)})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// aliasUnreadSubscriber registers a simulated client whose subscriptions are
// the signed Turbo::StreamsChannel aliases for streams, each resolved by
// Hub.subscription exactly as Serve resolves it. The subscribing client's user
// is deliberately independent of the streams' target users.
func aliasUnreadSubscriber(t testing.TB, hub *Hub, db *database.DB, secrets *rails.Secrets, user database.User, streams ...string) (*client, []string) {
	t.Helper()
	token, err := db.StartSession(context.Background(), user.ID, "alias", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c := simulateClient(t, user, token, "", subscription{})
	c.subscriptions = map[string]subscription{}
	identifiers := make([]string, 0, len(streams))
	for _, stream := range streams {
		identifier := signedAliasIdentifier(t, secrets, stream)
		sub, valid := hub.subscription(context.Background(), c, identifier)
		if !valid {
			t.Fatalf("signed alias for %q was rejected", stream)
		}
		c.subscriptions[identifier] = sub
		identifiers = append(identifiers, identifier)
	}
	hub.register(c)
	return c, identifiers
}

// strayUnreadSubscriber registers a simulated client whose user is NOT a
// member of any test room, subscribed to its own unread stream. It is built
// directly rather than through the database: the point is the hub's candidate
// set, and a real user row plus session per unrelated subscriber would turn
// the 10,000 case into a database benchmark instead of a hub one. Its token is
// deliberately not a live session, so a publication that looked it up would
// find it unauthorized and stop the client — which is how the tests below
// prove the token never reaches the query.
func strayUnreadSubscriber(hub *Hub, id int64, cancel context.CancelFunc) *client {
	user := database.User{ID: id}
	c := &client{
		user:          user,
		token:         fmt.Sprintf("stray-token-%d", id),
		cancel:        cancel,
		q:             newOutQueueCap(2),
		subscriptions: map[string]subscription{unreadIdentifier: unreadSubscription(user)},
	}
	hub.register(c)
	return c
}

// bumpPayload is the frame message the bump must carry: the room id.
func bumpPayload(room int64) string { return `{"roomId":` + strconv.FormatInt(room, 10) + `}` }

// expectBumpFor asserts c received exactly one bump frame for room under the
// given subscription identifier.
func expectBumpFor(t testing.TB, ctx context.Context, c *client, identifier string, room int64) {
	t.Helper()
	f := testRecv(ctx, c)
	if f == nil {
		t.Fatal("no unread bump delivered")
	}
	var frame struct {
		Identifier string          `json:"identifier"`
		Message    json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(f.Data(), &frame); err != nil {
		t.Fatalf("undecodable bump frame %q: %v", f.Data(), err)
	}
	if frame.Identifier != identifier {
		t.Fatalf("bump identifier = %q, want %q", frame.Identifier, identifier)
	}
	if string(frame.Message) != bumpPayload(room) {
		t.Fatalf("bump payload = %q, want %q", frame.Message, bumpPayload(room))
	}
}

// expectBump asserts c received exactly one bump frame for room under the
// canonical unread identifier.
func expectBump(t testing.TB, ctx context.Context, c *client, room int64) {
	t.Helper()
	expectBumpFor(t, ctx, c, unreadIdentifier, room)
}

// expectSilent asserts a subscriber received nothing and is still connected.
func expectSilent(t testing.TB, c *client) {
	t.Helper()
	expectNone(t, c)
	if c.dying.Load() {
		t.Fatal("subscriber was disconnected by a publication it was not an eligible recipient of")
	}
}

// removeMembership drops one membership row: through the audited helper (which
// bumps the local membership generation) or through a separate SQLite
// connection to the same file (a foreign writer, which bumps nothing local).
func removeMembership(t testing.TB, db *database.DB, room database.Room, user int64, foreign bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !foreign {
		members, err := db.RoomMemberIDs(ctx, room.ID)
		if err != nil {
			t.Fatal(err)
		}
		kept := make([]int64, 0, len(members))
		for _, member := range members {
			if member != user {
				kept = append(kept, member)
			}
		}
		if err := db.UpdateRoom(ctx, room.ID, room.Type, room.Name, kept); err != nil {
			t.Fatal(err)
		}
		return
	}
	var path string
	if err := db.Read.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "DELETE FROM memberships WHERE room_id=? AND user_id=?", room.ID, user); err != nil {
		t.Fatal(err)
	}
}

// TestUnreadBumpDeliveryMatrix pins the bump across subscriber counts
// (0/1/100) and room-member counts (2/100/1,000): every active subscriber that
// is a member receives exactly one frame, whatever the member count.
func TestUnreadBumpDeliveryMatrix(t *testing.T) {
	for _, members := range []int{2, 100, 1000} {
		for _, subs := range []int{0, 1, 100} {
			if subs > members {
				continue
			}
			t.Run(fmt.Sprintf("members=%d/subs=%d", members, subs), func(t *testing.T) {
				hub, db, owner, roomID, _ := hubFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				// CreateUser grants the new user a membership in every open
				// room (the fixture room is the open one), so the member
				// count is the number of users without touching memberships.
				users := []database.User{owner}
				for i := 1; i < members; i++ {
					u, err := db.CreateUser(ctx, fmt.Sprintf("Member%d", i), fmt.Sprintf("m%d@test", i), "digest", "", 0, nil)
					if err != nil {
						t.Fatal(err)
					}
					users = append(users, u)
				}
				clients := make([]*client, 0, subs)
				for i := 0; i < subs; i++ {
					c, _ := unreadSubscriber(t, hub, db, users[i])
					clients = append(clients, c)
				}
				if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
					t.Fatal(err)
				}
				for _, c := range clients {
					expectBump(t, ctx, c, roomID)
					expectNone(t, c) // exactly one frame per subscriber
				}
			})
		}
	}
}

// TestUnreadBumpRoomRelevantCandidates pins the candidate strategy with a fixed
// two-member target room and 0/100/10,000 unrelated subscribers: the statement
// count, the allocation count and the lock-held candidate work are flat in the
// unrelated count, the unrelated subscribers are never candidates (they are
// not delivered to and not disconnected, which a token list containing them
// would do, since their sessions do not exist), and the room's own subscriber
// still costs the member read plus the two eligibility rechecks.
func TestUnreadBumpRoomRelevantCandidates(t *testing.T) {
	var allocsByStrays []float64
	var straysByCount []int
	for _, strays := range []int{0, 100, 10000} {
		t.Run(fmt.Sprintf("strays=%d", strays), func(t *testing.T) {
			db, count, reset, _, err := database.OpenCounting(t.TempDir()+"/counted.sqlite3", 4)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			owner, err := db.Setup(ctx, "Owner", "owner@test", "digest")
			if err != nil {
				t.Fatal(err)
			}
			member, err := db.CreateUser(ctx, "Member", "member@test", "digest", "", 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			// The target: a closed room with exactly two members.
			pair, err := db.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Pair", []int64{owner.ID, member.ID})
			if err != nil {
				t.Fatal(err)
			}
			members, err := db.RoomMemberIDs(ctx, pair.ID)
			if err != nil || len(members) != 2 {
				t.Fatalf("target room members: %v %v", members, err)
			}
			secrets, err := rails.NewSecrets("unread-candidates")
			if err != nil {
				t.Fatal(err)
			}
			hub := New(db, secrets)
			t.Cleanup(hub.Close)
			_, cancelStrays := context.WithCancel(context.Background())
			defer cancelStrays()
			straysList := make([]*client, 0, strays)
			for i := 0; i < strays; i++ {
				straysList = append(straysList, strayUnreadSubscriber(hub, 1_000_000+int64(i), cancelStrays))
			}

			// 1. Nobody in the target room is subscribed. With no unread
			// subscriber anywhere the bump is the zero-cost shortcut; with
			// unrelated subscribers it is exactly the two-row member read,
			// flat in the unrelated count.
			wantStatements := int64(0)
			if strays > 0 {
				wantStatements = 1
			}
			reset()
			if err := hub.PublishUnreadBump(pair.ID, unreadBudget); err != nil {
				t.Fatal(err)
			}
			// Behaviour first: an unrelated subscriber is not a candidate, so
			// it is neither delivered to nor disconnected (its token is not a
			// live session, so a publication that looked it up would stop it).
			for _, c := range straysList {
				expectSilent(t, c)
			}
			if got := count(); got != wantStatements {
				t.Fatalf("bump statements = %d, want %d (member read only)", got, wantStatements)
			}
			if strays > 0 {
				allocsByStrays = append(allocsByStrays, testing.AllocsPerRun(200, func() {
					if err := hub.PublishUnreadBump(pair.ID, unreadBudget); err != nil {
						t.Fatal(err)
					}
				}))
				straysByCount = append(straysByCount, strays)
			}

			// 2. The room's member subscribes: the member read plus the two
			// eligibility rechecks, still flat in the unrelated count, and only
			// the member is delivered to.
			memberC, _ := unreadSubscriber(t, hub, db, member)
			reset()
			if err := hub.PublishUnreadBump(pair.ID, unreadBudget); err != nil {
				t.Fatal(err)
			}
			expectBump(t, ctx, memberC, pair.ID)
			expectNone(t, memberC)
			for _, c := range straysList {
				expectSilent(t, c)
			}
			if got := count(); got != 3 {
				t.Fatalf("bump statements = %d, want 3 (member read + membership recheck + session check)", got)
			}
		})
	}
	// The allocation count of the candidate work must not move between 100 and
	// 10,000 unrelated subscribers: neither the member read, the token list,
	// the target list nor the snapshot touches them. A per-subscriber
	// implementation would add thousands here (one token each, plus the JSON
	// array); the slack absorbs the runtime's own measurement noise under
	// -race, and the absolute bound keeps the slack from hiding a real
	// regression. The delivery assertions above are the exact half of this pin.
	if len(allocsByStrays) != 2 || allocsByStrays[1] > allocsByStrays[0]+4 || allocsByStrays[1] > 200 {
		t.Fatalf("candidate allocations grew with unrelated subscribers: %v (counts %v)", allocsByStrays, straysByCount)
	}
}

// TestUnreadBumpAuthorizationRecheck pins the session half of the eligibility
// recheck: a subscriber whose session was revoked is excluded and stopped (the
// generic publish path's rule for an unauthorized recipient), while a live
// subscriber that is not a member of the posted room is not a candidate at all
// — it receives nothing and stays connected.
func TestUnreadBumpAuthorizationRecheck(t *testing.T) {
	hub, db, owner, _, _ := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	revoked, err := db.CreateUser(ctx, "Revoked", "revoked@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A closed room with the owner and the soon-to-be-revoked user as members;
	// the outsider is a member of the open room only, never of this one.
	closed, err := db.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Quiet", []int64{owner.ID, revoked.ID})
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := db.CreateUser(ctx, "Outsider", "outsider@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	memberC, _ := unreadSubscriber(t, hub, db, owner)
	nonMemberC, _ := unreadSubscriber(t, hub, db, outsider)
	revokedC, revokedToken := unreadSubscriber(t, hub, db, revoked)
	if err := db.DeleteSession(ctx, revokedToken, revoked.ID); err != nil {
		t.Fatal(err)
	}

	if err := hub.PublishUnreadBump(closed.ID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	expectBump(t, ctx, memberC, closed.ID)
	expectSilent(t, nonMemberC)
	expectNone(t, revokedC)
	if !revokedC.dying.Load() {
		t.Fatal("revoked subscriber was not disconnected by the bump")
	}
}

// TestUnreadBumpMembershipRemovalBetweenReads pins the membership half of the
// eligibility recheck, deterministically on the publication's own boundary: the
// membership is removed after the candidate read (through the test seam, which
// runs after the recipient snapshot) and before the eligibility reads, both via
// the audited helper and via a separate SQLite connection that bumps no local
// generation. The removed member must receive no frame and stay connected,
// while a still-member control keeps receiving.
func TestUnreadBumpMembershipRemovalBetweenReads(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign=%t", foreign), func(t *testing.T) {
			hub, db, owner, _, _ := hubFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			removed, err := db.CreateUser(ctx, "Removed", "removed@test", "digest", "", 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			stay, err := db.CreateUser(ctx, "Stay", "stay@test", "digest", "", 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			closed, err := db.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Pair", []int64{owner.ID, removed.ID, stay.ID})
			if err != nil {
				t.Fatal(err)
			}
			removedC, _ := unreadSubscriber(t, hub, db, removed)
			stayC, _ := unreadSubscriber(t, hub, db, stay)

			unreadBumpAfterSnapshot = func() {
				unreadBumpAfterSnapshot = nil
				removeMembership(t, db, closed, removed.ID, foreign)
			}
			t.Cleanup(func() { unreadBumpAfterSnapshot = nil })
			if err := hub.PublishUnreadBump(closed.ID, unreadBudget); err != nil {
				t.Fatal(err)
			}

			expectSilent(t, removedC)
			expectBump(t, ctx, stayC, closed.ID)
			expectNone(t, stayC)
		})
	}
}

// TestUnreadBumpSignedAliasParity pins the index's identity rule: the unread
// stream NAME is the subscription, so a signed Turbo::StreamsChannel alias for
// `user_<id>_unreads` is a recipient exactly like the canonical
// UnreadRoomsChannel subscription, with the same identifiers and the same
// recipient set as a generic publication to that stream — including when the
// subscribing client's user is not the stream's target user, which is what
// makes the index key the name rather than the client.
func TestUnreadBumpSignedAliasParity(t *testing.T) {
	hub, db, owner, _, secrets := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	member, err := db.CreateUser(ctx, "Member", "member@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := db.CreateUser(ctx, "Outsider", "outsider@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The target room holds the member and the owner; the outsider is a member
	// of the open room only, so an alias it holds cannot be a candidate by its
	// own membership.
	pair, err := db.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Pair", []int64{owner.ID, member.ID})
	if err != nil {
		t.Fatal(err)
	}
	stream := unreadStreamName(member.ID)
	canonical, _ := unreadSubscriber(t, hub, db, member)
	canonicalID := unreadIdentifier
	alias, aliasIDs := aliasUnreadSubscriber(t, hub, db, secrets, member, stream)
	aliasID := aliasIDs[0]
	foreignAlias, foreignAliasIDs := aliasUnreadSubscriber(t, hub, db, secrets, outsider, stream)
	foreignAliasID := foreignAliasIDs[0]

	// The generic publication to the same stream name is the control: the bump
	// must reach the same clients under the same identifiers.
	hub.PublishStream(ctx, stream, map[string]any{"roomId": pair.ID})
	expectBumpFor(t, ctx, canonical, canonicalID, pair.ID)
	expectBumpFor(t, ctx, alias, aliasID, pair.ID)
	expectBumpFor(t, ctx, foreignAlias, foreignAliasID, pair.ID)

	if err := hub.PublishUnreadBump(pair.ID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	expectBumpFor(t, ctx, canonical, canonicalID, pair.ID)
	expectBumpFor(t, ctx, alias, aliasID, pair.ID)
	expectBumpFor(t, ctx, foreignAlias, foreignAliasID, pair.ID)

	// An alias released by unsubscribe is no longer a recipient of either
	// publication; the others keep both.
	aliasSub := alias.subscriptions[aliasID]
	hub.mu.Lock()
	delete(alias.subscriptions, aliasID)
	hub.indexRemoveLocked(alias, aliasID, aliasSub)
	hub.mu.Unlock()
	hub.PublishStream(ctx, stream, map[string]any{"roomId": pair.ID})
	expectSilent(t, alias)
	expectBumpFor(t, ctx, canonical, canonicalID, pair.ID)
	expectBumpFor(t, ctx, foreignAlias, foreignAliasID, pair.ID)
	if err := hub.PublishUnreadBump(pair.ID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	expectSilent(t, alias)
	expectBumpFor(t, ctx, canonical, canonicalID, pair.ID)
	expectBumpFor(t, ctx, foreignAlias, foreignAliasID, pair.ID)
}

// TestUnreadBumpNoncanonicalAliasParity pins stream identity exactly. ParseInt
// accepts `01` and `+1`, but the generic publication matches the exact stream
// name, so a signed alias for `user_01_unreads` or `user_+1_unreads` is a
// different subscription from `user_1_unreads`: the bump must not reach it
// (which would expand user 1's recipient set beyond what the generic path
// delivers to), while the alias stays a perfectly valid generic subscription
// to its own name. Each client is checked for the exact identifier and the
// exact frame count of both publications.
func TestUnreadBumpNoncanonicalAliasParity(t *testing.T) {
	hub, db, owner, _, secrets := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	member, err := db.CreateUser(ctx, "Member", "member@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := db.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Pair", []int64{owner.ID, member.ID})
	if err != nil {
		t.Fatal(err)
	}
	canonicalStream := unreadStreamName(member.ID)
	zeroStream := fmt.Sprintf("user_0%d_unreads", member.ID)
	plusStream := fmt.Sprintf("user_+%d_unreads", member.ID)

	canonicalAlias, canonicalIDs := aliasUnreadSubscriber(t, hub, db, secrets, member, canonicalStream)
	zeroAlias, zeroIDs := aliasUnreadSubscriber(t, hub, db, secrets, member, zeroStream)
	plusAlias, plusIDs := aliasUnreadSubscriber(t, hub, db, secrets, member, plusStream)
	// One client holding the canonical name and a noncanonical one together.
	both, bothIDs := aliasUnreadSubscriber(t, hub, db, secrets, member, canonicalStream, zeroStream)
	bothCanonicalID, bothZeroID := bothIDs[0], bothIDs[1]
	payload := map[string]any{"roomId": pair.ID}

	// 1. The bump: exactly the canonical subscriptions, one frame each.
	if err := hub.PublishUnreadBump(pair.ID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	expectBumpFor(t, ctx, canonicalAlias, canonicalIDs[0], pair.ID)
	expectBumpFor(t, ctx, both, bothCanonicalID, pair.ID)
	expectSilent(t, zeroAlias)
	expectSilent(t, plusAlias)
	expectNone(t, both) // the noncanonical identifier got no frame

	// 2. The generic publication to the canonical name: the same set.
	hub.PublishStream(ctx, canonicalStream, payload)
	expectBumpFor(t, ctx, canonicalAlias, canonicalIDs[0], pair.ID)
	expectBumpFor(t, ctx, both, bothCanonicalID, pair.ID)
	expectSilent(t, zeroAlias)
	expectSilent(t, plusAlias)
	expectNone(t, both)

	// 3. Generic acceptance is unchanged: each noncanonical alias still
	// receives a publication to its own exact name, and only that one.
	hub.PublishStream(ctx, zeroStream, payload)
	expectBumpFor(t, ctx, zeroAlias, zeroIDs[0], pair.ID)
	expectBumpFor(t, ctx, both, bothZeroID, pair.ID)
	expectSilent(t, canonicalAlias)
	expectSilent(t, plusAlias)
	expectNone(t, both)

	hub.PublishStream(ctx, plusStream, payload)
	expectBumpFor(t, ctx, plusAlias, plusIDs[0], pair.ID)
	expectSilent(t, canonicalAlias)
	expectSilent(t, zeroAlias)
	expectNone(t, both)
}

// TestUnreadBumpZeroAndNegativeTargetParity pins that the identity rule is the
// exact stream name and nothing else. The users table's integer primary key
// admits 0 and negative rowids, and a membership may reference them, so
// `user_0_unreads` and `user_-1_unreads` are real streams: a positive-ID client
// holding a signed alias for one must be a recipient exactly when the generic
// publication to that name is. The noncanonical spellings of the same targets
// (`user_+0_unreads`, `user_-0_unreads`) stay excluded, as `+1` and `01` do in
// TestUnreadBumpNoncanonicalAliasParity.
func TestUnreadBumpZeroAndNegativeTargetParity(t *testing.T) {
	hub, db, owner, _, secrets := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	targets := []int64{0, -1}
	for _, id := range targets {
		if _, err := db.Write.ExecContext(ctx,
			"INSERT INTO users(id,name,role,status,created_at,updated_at) VALUES(?,?,0,0,'now','now')",
			id, fmt.Sprintf("Target%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	closed, err := db.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Edge", []int64{owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range targets {
		if _, err := db.Write.ExecContext(ctx,
			"INSERT INTO memberships(room_id,user_id,involvement,created_at,updated_at) VALUES(?,?,'everything','now','now')",
			closed.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	members, err := db.RoomMemberIDs(ctx, closed.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range targets {
		if !slices.Contains(members, id) {
			t.Fatalf("membership for target %d was not stored: %v", id, members)
		}
	}
	// The subscriber is a positive-ID user; only the stream name names the
	// target.
	zeroAlias, zeroIDs := aliasUnreadSubscriber(t, hub, db, secrets, owner, unreadStreamName(0))
	negAlias, negIDs := aliasUnreadSubscriber(t, hub, db, secrets, owner, unreadStreamName(-1))
	plusZero, plusZeroIDs := aliasUnreadSubscriber(t, hub, db, secrets, owner, "user_+0_unreads")
	negZero, negZeroIDs := aliasUnreadSubscriber(t, hub, db, secrets, owner, "user_-0_unreads")
	payload := map[string]any{"roomId": closed.ID}

	// 1. The bump reaches the canonical spellings, whose targets are members.
	if err := hub.PublishUnreadBump(closed.ID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	expectBumpFor(t, ctx, zeroAlias, zeroIDs[0], closed.ID)
	expectBumpFor(t, ctx, negAlias, negIDs[0], closed.ID)
	expectSilent(t, plusZero)
	expectSilent(t, negZero)

	// 2. Generic parity, name for name.
	for _, c := range []struct {
		stream string
		client *client
		id     string
	}{
		{unreadStreamName(0), zeroAlias, zeroIDs[0]},
		{unreadStreamName(-1), negAlias, negIDs[0]},
		{"user_+0_unreads", plusZero, plusZeroIDs[0]},
		{"user_-0_unreads", negZero, negZeroIDs[0]},
	} {
		hub.PublishStream(ctx, c.stream, payload)
		expectBumpFor(t, ctx, c.client, c.id, closed.ID)
		expectSilent(t, plusZero)
		expectSilent(t, negZero)
		expectSilent(t, zeroAlias)
		expectSilent(t, negAlias)
	}
}

// unreadPoisoningFixture is a closed two-member room whose members both hold an
// unread subscription, shared by the scratch-reuse cases below.
type unreadPoisoningFixture struct {
	hub     *Hub
	db      *database.DB
	room    database.Room
	first   database.User
	firstC  *client
	firstT  string
	second  database.User
	secondC *client
	secondT string
}

func newUnreadPoisoningFixture(t testing.TB) unreadPoisoningFixture {
	t.Helper()
	hub, db, owner, _, _ := hubFixture(t)
	ctx := context.Background()
	first, err := db.CreateUser(ctx, "First", "first@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateUser(ctx, "Second", "second@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := db.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Pair", []int64{owner.ID, first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	firstC, firstT := unreadSubscriber(t, hub, db, first)
	secondC, secondT := unreadSubscriber(t, hub, db, second)
	return unreadPoisoningFixture{hub: hub, db: db, room: closed, first: first, firstC: firstC, firstT: firstT, second: second, secondC: secondC, secondT: secondT}
}

// unreadCandidate is one member's subscription as the snapshot would have
// recorded it.
type unreadCandidate struct {
	user   database.User
	client *client
	token  string
}

func (f unreadPoisoningFixture) firstCandidate() unreadCandidate {
	return unreadCandidate{user: f.first, client: f.firstC, token: f.firstT}
}

func (f unreadPoisoningFixture) secondCandidate() unreadCandidate {
	return unreadCandidate{user: f.second, client: f.secondC, token: f.secondT}
}

// scratchWith builds a scratch exactly as an earlier publication would have
// left it: fresh still holds stale as eligible, and the candidate buffers hold
// exactly candidates. The test owns the object, so the reuse is deterministic
// and does not depend on sync.Pool handing back the same one.
func (f unreadPoisoningFixture) scratchWith(stale []int64, candidates ...unreadCandidate) *publishScratch {
	s := &publishScratch{fresh: append([]int64{}, stale...)}
	for _, c := range candidates {
		s.recips = append(s.recips, recipient{client: c.client, identifier: unreadIdentifier, room: f.room.ID, scope: roomScope(f.room.ID), target: c.user.ID})
		s.tokens = append(s.tokens, c.token)
		s.targets = append(s.targets, c.user.ID)
	}
	return s
}

// TestUnreadBumpEligibilityScratchPoisoning pins the pooled result buffer's
// reuse. The membership recheck appends into scratch.fresh, so a target left
// eligible by an earlier publication must not survive into a later one; each
// case starts from a scratch whose fresh already holds both targets and asserts
// the refilled result, so removing the truncation fails every case on its own
// assertion rather than being masked by an earlier one.
func TestUnreadBumpEligibilityScratchPoisoning(t *testing.T) {
	t.Run("refill", func(t *testing.T) {
		f := newUnreadPoisoningFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		scratch := f.scratchWith([]int64{f.first.ID, f.second.ID}, f.firstCandidate(), f.secondCandidate())
		kept, err := f.hub.unreadEligible(ctx, scratch, f.room.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 2 {
			t.Fatalf("eligible recipients = %d, want 2", len(kept))
		}
		if len(scratch.fresh) != 2 {
			t.Fatalf("result buffer = %v, want both targets", scratch.fresh)
		}
	})

	t.Run("smaller", func(t *testing.T) {
		f := newUnreadPoisoningFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// The candidate set is smaller than the stale result: only the second
		// member is a candidate, and it is still a member.
		scratch := f.scratchWith([]int64{f.first.ID, f.second.ID}, f.secondCandidate())
		kept, err := f.hub.unreadEligible(ctx, scratch, f.room.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 1 || kept[0].target != f.second.ID {
			t.Fatalf("eligible recipients = %v, want only target %d", kept, f.second.ID)
		}
		if len(scratch.fresh) != 1 || scratch.fresh[0] != f.second.ID {
			t.Fatalf("result buffer = %v, want exactly target %d (the stale target must not be retained)", scratch.fresh, f.second.ID)
		}
	})

	t.Run("removal", func(t *testing.T) {
		f := newUnreadPoisoningFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// The stale result holds both targets eligible; the only candidate is
		// the member whose membership is removed before the recheck.
		scratch := f.scratchWith([]int64{f.first.ID, f.second.ID}, f.secondCandidate())
		removeMembership(t, f.db, f.room, f.second.ID, false)
		if len(scratch.fresh) != 2 {
			t.Fatalf("premise: the result buffer should still hold the stale pair, got %v", scratch.fresh)
		}
		kept, err := f.hub.unreadEligible(ctx, scratch, f.room.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 0 || len(scratch.fresh) != 0 {
			t.Fatalf("removed target survived the recheck: kept=%v fresh=%v", kept, scratch.fresh)
		}
		expectSilent(t, f.secondC)
	})

	t.Run("survivor-frame", func(t *testing.T) {
		f := newUnreadPoisoningFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// The survivor of the stale pair is still eligible, and its frame is
		// the only one delivered. This case pins the delivered frame rather
		// than the buffer's length: its target is in the stale set either way,
		// so it is not a truncation detector — refill, smaller and removal are.
		scratch := f.scratchWith([]int64{f.first.ID, f.second.ID}, f.firstCandidate())
		kept, err := f.hub.unreadEligible(ctx, scratch, f.room.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 1 || kept[0].target != f.first.ID {
			t.Fatalf("eligible recipients = %v, want only target %d", kept, f.first.ID)
		}
		scratch.wrap = f.hub.fanout(kept, map[string]any{"roomId": f.room.ID}, scratch.wrap)
		expectBump(t, ctx, f.firstC, f.room.ID)
		expectNone(t, f.firstC)
		expectSilent(t, f.secondC)
	})
}

// TestUnreadBumpScratchReset pins publishScratch.reset with every buffer
// populated: a scratch taken from the pool must carry no length from the
// previous publication. Each length assertion is the one that fails if reset
// stops truncating that buffer, and the capacity assertions pin that reset
// truncates rather than discards. The reset scratch is then refilled and
// resolved, so the test ends on the reusable state rather than the truncation
// alone.
func TestUnreadBumpScratchReset(t *testing.T) {
	hub, db, owner, _, _ := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	member, err := db.CreateUser(ctx, "Member", "member@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := db.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Pair", []int64{owner.ID, member.ID})
	if err != nil {
		t.Fatal(err)
	}
	c, token := unreadSubscriber(t, hub, db, member)
	scratch := &publishScratch{
		wrap:    []byte(`{"stale":true}`),
		recips:  []recipient{{client: c, identifier: unreadIdentifier, room: closed.ID, scope: roomScope(closed.ID), target: member.ID}},
		tokens:  []string{token},
		targets: []int64{member.ID},
		fresh:   []int64{member.ID},
	}
	scratch.reset()
	if len(scratch.wrap) != 0 || len(scratch.recips) != 0 || len(scratch.tokens) != 0 || len(scratch.targets) != 0 || len(scratch.fresh) != 0 {
		t.Fatalf("reset left lengths: wrap=%d recips=%d tokens=%d targets=%d fresh=%d",
			len(scratch.wrap), len(scratch.recips), len(scratch.tokens), len(scratch.targets), len(scratch.fresh))
	}
	if cap(scratch.wrap) == 0 || cap(scratch.recips) == 0 || cap(scratch.tokens) == 0 || cap(scratch.targets) == 0 || cap(scratch.fresh) == 0 {
		t.Fatalf("reset discarded a buffer instead of truncating it: wrap=%d recips=%d tokens=%d targets=%d fresh=%d",
			cap(scratch.wrap), cap(scratch.recips), cap(scratch.tokens), cap(scratch.targets), cap(scratch.fresh))
	}

	// The reset scratch is reusable: refill the candidates and resolve.
	scratch.recips = append(scratch.recips, recipient{client: c, identifier: unreadIdentifier, room: closed.ID, scope: roomScope(closed.ID), target: member.ID})
	scratch.tokens = append(scratch.tokens, token)
	scratch.targets = append(scratch.targets, member.ID)
	kept, err := hub.unreadEligible(ctx, scratch, closed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].target != member.ID {
		t.Fatalf("eligible recipients after reset = %v, want target %d", kept, member.ID)
	}
	scratch.wrap = hub.fanout(kept, map[string]any{"roomId": closed.ID}, scratch.wrap)
	expectBump(t, ctx, c, closed.ID)
	expectNone(t, c)
}

// TestUnreadBumpSignedAliasSocketLifecycle drives a signed alias through the
// real Serve path: the subscription is accepted, the bump reaches it on the
// wire under its own identifier, and closing the connection releases the index
// entry, so the next publication has no recipient for that stream.
func TestUnreadBumpSignedAliasSocketLifecycle(t *testing.T) {
	hub, _, user, roomID, secrets, url := socketHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http"), &websocket.DialOptions{
		Subprotocols:    []string{"actioncable-v1-json"},
		CompressionMode: websocket.CompressionNoContextTakeover,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var welcome map[string]any
	if err := wsjsonRead(ctx, conn, &welcome); err != nil || welcome["type"] != "welcome" {
		t.Fatalf("welcome: %v %v", welcome, err)
	}
	identifier := signedAliasIdentifier(t, secrets, unreadStreamName(user.ID))
	if err := wsjsonWrite(ctx, conn, map[string]string{"command": "subscribe", "identifier": identifier}); err != nil {
		t.Fatal(err)
	}
	for {
		var got map[string]any
		if err := wsjsonRead(ctx, conn, &got); err != nil {
			t.Fatal(err)
		}
		if got["type"] == "confirm_subscription" {
			break
		}
	}
	if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	for {
		var frame struct {
			Type       string          `json:"type"`
			Identifier string          `json:"identifier"`
			Message    json.RawMessage `json:"message"`
		}
		if err := wsjsonRead(ctx, conn, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type != "" {
			continue // welcome, confirm_subscription, ping
		}
		if frame.Identifier != identifier || string(frame.Message) != bumpPayload(roomID) {
			t.Fatalf("alias frame = %q / %q", frame.Identifier, frame.Message)
		}
		break
	}
	conn.CloseNow()
	deadline := time.Now().Add(5 * time.Second)
	for {
		hub.mu.RLock()
		remaining := len(hub.unreadByUser)
		hub.mu.RUnlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected alias stayed indexed")
		}
		time.Sleep(time.Millisecond)
	}
	// With the alias gone the publication has no recipient at all.
	if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
		t.Fatal(err)
	}
}

// TestUnreadBumpZeroSubscriberShortcut pins the shortcut: with no unread
// subscription anywhere the bump sees an empty index and returns — zero
// statements and zero allocations per call, no timer.
func TestUnreadBumpZeroSubscriberShortcut(t *testing.T) {
	hub, _, _, roomID, _ := hubFixture(t)
	if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
			t.Fatal(err)
		}
	}); allocs != 0 {
		t.Fatalf("zero-subscriber unread bump = %.2f allocs/op, want 0", allocs)
	}
}

// TestUnreadBumpStatementBudget pins the statement count of one bump: zero for
// a server with no unread subscriber, and for a room whose member is the only
// subscriber exactly the member read plus the two eligibility rechecks (the
// membership recheck and the session check) — never one statement per member.
func TestUnreadBumpStatementBudget(t *testing.T) {
	db, count, reset, dump, err := database.OpenCounting(t.TempDir()+"/counted.sqlite3", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner, err := db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(ctx, owner.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatalf("rooms: %v %v", rooms, err)
	}
	secrets, err := rails.NewSecrets("unread-bump-counted")
	if err != nil {
		t.Fatal(err)
	}
	hub := New(db, secrets)
	t.Cleanup(hub.Close)

	reset()
	if err := hub.PublishUnreadBump(rooms[0].ID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 0 {
		t.Fatalf("zero-subscriber bump statements = %d, want 0\n%s", got, strings.Join(dump(), "\n"))
	}

	// A 100-member room with one subscriber still costs the member read plus
	// two eligibility rechecks, not one publish per member.
	for i := 1; i < 100; i++ {
		if _, err := db.CreateUser(ctx, fmt.Sprintf("Member%d", i), fmt.Sprintf("m%d@test", i), "digest", "", 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	unreadSubscriber(t, hub, db, owner)
	reset()
	if err := hub.PublishUnreadBump(rooms[0].ID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 3 {
		t.Fatalf("one-subscriber bump statements = %d, want 3 (member read + membership recheck + session check)\n%s", got, strings.Join(dump(), "\n"))
	}
}

// TestUnreadBumpConcurrencyPoint pins the shortcut's two-sided linearization:
// a subscription registered before the bump is delivered to and one removed
// before the bump is not.
func TestUnreadBumpConcurrencyPoint(t *testing.T) {
	hub, db, owner, roomID, _ := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, _ := unreadSubscriber(t, hub, db, owner)

	if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	expectBump(t, ctx, c, roomID)

	hub.mu.Lock()
	delete(c.subscriptions, unreadIdentifier)
	hub.indexRemoveLocked(c, unreadIdentifier, unreadSubscription(owner))
	hub.mu.Unlock()
	if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	expectNone(t, c)

	// Re-subscribing restores delivery (the index is the only gate).
	hub.mu.Lock()
	c.subscriptions[unreadIdentifier] = unreadSubscription(owner)
	hub.indexAddLocked(c, unreadIdentifier, unreadSubscription(owner))
	hub.mu.Unlock()
	if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	expectBump(t, ctx, c, roomID)
}

// TestUnreadBumpSnapshotBoundary pins the publication's concurrency point with
// an ACTUAL publication: the seam holds the publication open right after its
// recipient snapshot, a subscriber registered there is excluded from this
// event and included in the next one, and the already-registered subscriber is
// unaffected. The churn test below exercises the same invariant statistically
// under -race.
func TestUnreadBumpSnapshotBoundary(t *testing.T) {
	hub, db, owner, roomID, _ := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first, _ := unreadSubscriber(t, hub, db, owner)
	// A client with a session, not yet registered with the hub.
	lateToken, err := db.StartSession(ctx, owner.ID, "late", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	late := simulateClient(t, owner, lateToken, unreadIdentifier, unreadSubscription(owner))

	atSnapshot := make(chan struct{})
	proceed := make(chan struct{})
	unreadBumpAfterSnapshot = func() {
		close(atSnapshot)
		<-proceed
	}
	t.Cleanup(func() { unreadBumpAfterSnapshot = nil })
	done := make(chan error, 1)
	go func() { done <- hub.PublishUnreadBump(roomID, unreadBudget) }()
	<-atSnapshot // this publication has taken its snapshot
	unreadBumpAfterSnapshot = nil
	hub.register(late) // lands after the snapshot
	close(proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// This event: the pre-snapshot subscriber only.
	expectBump(t, ctx, first, roomID)
	expectNone(t, late)

	// The next event: both.
	if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	expectBump(t, ctx, first, roomID)
	expectBump(t, ctx, late, roomID)
	expectNone(t, first)
	expectNone(t, late)
}

// TestUnreadBumpShortcutReturnsBeforeSnapshot pins the other side of the
// boundary separately: with no unread subscription the publication returns on
// the index check and never reaches the snapshot seam at all.
func TestUnreadBumpShortcutReturnsBeforeSnapshot(t *testing.T) {
	hub, _, _, roomID, _ := hubFixture(t)
	reached := false
	unreadBumpAfterSnapshot = func() { reached = true }
	t.Cleanup(func() { unreadBumpAfterSnapshot = nil })
	if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	unreadBumpAfterSnapshot = nil
	if reached {
		t.Fatal("the zero-subscriber shortcut reached the recipient snapshot")
	}
}

// TestUnreadBumpConcurrentChurn exercises the bump against concurrent
// subscribe/unsubscribe churn under -race: every frame any client observes
// must be a bump frame, and once the churn has stopped a final bump must reach
// every client that is still subscribed (the index and the client set stay
// consistent).
func TestUnreadBumpConcurrentChurn(t *testing.T) {
	hub, db, owner, roomID, _ := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	const clientsN, bumpers, perBumper = 32, 3, 40
	clients := make([]*client, 0, clientsN)
	for i := 0; i < clientsN; i++ {
		c, _ := unreadSubscriber(t, hub, db, owner)
		clients = append(clients, c)
	}
	want := bumpPayload(roomID)
	var drainWG sync.WaitGroup
	stormOver := make(chan struct{})
	for _, c := range clients {
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
				var frame struct {
					Identifier string          `json:"identifier"`
					Message    json.RawMessage `json:"message"`
				}
				if err := json.Unmarshal(f.Data(), &frame); err != nil || frame.Identifier != unreadIdentifier || string(frame.Message) != want {
					t.Errorf("client %p received non-bump frame %q", c, f.Data())
				}
			}
		}(c)
	}
	var wg sync.WaitGroup
	for i := 0; i < bumpers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perBumper; j++ {
				if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		sub := unreadSubscription(owner)
		for j := 0; j < perBumper; j++ {
			for _, c := range clients {
				hub.mu.Lock()
				delete(c.subscriptions, unreadIdentifier)
				hub.indexRemoveLocked(c, unreadIdentifier, sub)
				hub.mu.Unlock()
			}
			for _, c := range clients {
				hub.mu.Lock()
				c.subscriptions[unreadIdentifier] = sub
				hub.indexAddLocked(c, unreadIdentifier, sub)
				hub.mu.Unlock()
			}
		}
	}()
	wg.Wait()
	close(stormOver)
	drainWG.Wait()
	// A drainer's final empty check can lose the race with a bump that landed
	// just before the storm ended, so clear whatever storm frames are left.
	for _, c := range clients {
		c.q.clear()
	}
	// Every client is subscribed again: the final bump reaches all of them.
	if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
		t.Fatal(err)
	}
	for _, c := range clients {
		expectBump(t, ctx, c, roomID)
		expectNone(t, c)
	}
}
