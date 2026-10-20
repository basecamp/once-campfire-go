package application

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/jobs"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

type pushTestResolver struct{}

func (pushTestResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

type pushTestTransport func(*http.Request) (*http.Response, error)

func (f pushTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exhaust unread selection's real budget, then restore the reader. Push must
// still select and admit its owned delivery; only the provider boundary is fake.
func TestUnreadTimeoutDoesNotAbandonPush(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := database.Open(filepath.Join(t.TempDir(), "notifications.sqlite3"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := db.CreateUser(ctx, owner.ID, database.UserInput{Name: "Recipient", Email: "recipient@test"})
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	if _, err := db.Write.ExecContext(ctx, "UPDATE memberships SET involvement='everything' WHERE user_id=? AND room_id=?", recipient.User.ID, room.ID); err != nil {
		t.Fatal(err)
	}
	body := "Hello"
	commit, err := db.CreateMessage(ctx, owner.ID, room.ID, database.MessageInput{Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	vapid, err := integrations.NewVAPID("mailto:test@example.com", encode(key.PublicKey().Bytes()), encode(key.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, publicKey, auth := "https://fcm.googleapis.com/send/test", encode(key.PublicKey().Bytes()), encode(make([]byte, 16))
	if err := db.SavePushSubscription(ctx, recipient.User.ID, map[string]*string{"endpoint": &endpoint, "p256dh_key": &publicKey, "auth_key": &auth}, "test"); err != nil {
		t.Fatal(err)
	}
	secrets, err := rails.NewSecrets("notification-test")
	if err != nil {
		t.Fatal(err)
	}
	hub := cable.New(db, secrets)
	defer hub.Close()
	runner := jobs.New(1, "push")
	defer runner.Close(time.Second)
	var deliveries atomic.Int32
	push := integrations.NewPushSender(vapid)
	push.Resolver = pushTestResolver{}
	push.Client = &http.Client{Transport: pushTestTransport(func(*http.Request) (*http.Response, error) {
		deliveries.Add(1)
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	notifications := MessageNotifications{DB: db, Cable: hub, Jobs: runner, Push: push,
		Queries: &NotificationQueries{DB: db, Content: &ContentQueries{DB: db, Secrets: secrets}}}
	reader, err := db.Read.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	before := db.Read.Stats().WaitDuration
	released := make(chan struct{})
	go func() {
		defer close(released)
		for db.Read.Stats().WaitDuration == before {
			select {
			case <-ctx.Done():
				reader.Close()
				return
			case <-time.After(time.Millisecond):
			}
		}
		// WaitDuration advances only when the blocked selection finishes.
		reader.Close()
	}()
	notifications.Created(commit.Message, room)
	<-released
	runner.Close(time.Second)
	if got := deliveries.Load(); got != 1 {
		t.Fatalf("push deliveries after unread timeout: %d, want 1", got)
	}
}

func TestCompressedGoneResponseDeletesOnlySelectedSubscription(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(filepath.Join(t.TempDir(), "push-gone.sqlite3"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, owner.ID, database.UserInput{Name: "Other", Email: "other@test"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	publicKey, auth := encode(key.PublicKey().Bytes()), encode(make([]byte, 16))
	endpoint := "https://fcm.googleapis.com/send/test"
	for _, user := range []int64{owner.ID, other.ID} {
		if err := db.SavePushSubscription(ctx, user, map[string]*string{"endpoint": &endpoint, "p256dh_key": &publicKey, "auth_key": &auth}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	vapid, err := integrations.NewVAPID("mailto:test@example.com", publicKey, encode(key.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusGone) // No gzip header/body; status alone expires it.
	}))
	defer provider.Close()
	target, err := url.Parse(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	wire := integrations.HTTPClient(false, time.Second, nil)
	push := integrations.NewPushSender(vapid)
	push.Resolver = pushTestResolver{}
	push.Client = &http.Client{Transport: pushTestTransport(func(request *http.Request) (*http.Response, error) {
		// Change only network routing to the controlled provider. Keep the real
		// HTTP exchange, transparent decoder and encrypted push/status handling.
		request = request.Clone(request.Context())
		request.URL.Scheme, request.URL.Host = target.Scheme, target.Host
		return wire.Transport.RoundTrip(request)
	})}
	list, err := db.PushSubscriptions(ctx, owner.ID)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	task := pushTask{notifications: &MessageNotifications{DB: db, Push: push}, delivery: PushDelivery{Subscription: list[0], Payload: []byte(`{"title":"test"}`)}}
	if err := task.Run(ctx); err != nil {
		t.Error(err)
	}
	for _, user := range []int64{owner.ID, other.ID} {
		list, err := db.PushSubscriptions(ctx, user)
		want := 0
		if user == other.ID {
			want = 1
		}
		if err != nil || len(list) != want {
			t.Fatalf("user %d subscriptions: %v %v, want %d", user, list, err, want)
		}
	}
}
