package web

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/jobs"
)

type outboxTransport struct {
	status atomic.Int64
	calls  atomic.Int64
}
type unavailablePushDNS struct{}

func (unavailablePushDNS) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, errors.New("temporary DNS failure")
}
func (x *outboxTransport) RoundTrip(*http.Request) (*http.Response, error) {
	x.calls.Add(1)
	return &http.Response{StatusCode: int(x.status.Load()), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestPushOutboxRetriesAndStartupReplays(t *testing.T) {
	app, _, _, owner := testApp(t)
	app.Jobs.Close(time.Second)
	ctx := context.Background()
	recipient, err := app.DB.CreateUser(ctx, "Recipient", "outbox@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := app.DB.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	if err = app.DB.SetInvolvement(ctx, recipient.ID, room.ID, "everything"); err != nil {
		t.Fatal(err)
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Write.ExecContext(ctx, "INSERT INTO push_subscriptions(user_id,endpoint,p256dh_key,auth_key,user_agent,created_at,updated_at) VALUES(?,?,?,?,?,'now','now')", recipient.ID, "https://fcm.googleapis.com/fcm/send/outbox", base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(make([]byte, 16)), "test")
	if err != nil {
		t.Fatal(err)
	}
	vapid, err := integrations.NewVAPID("mailto:test@test", "BCx3gcVTvPQLdipX32-QcGJFqD_JISKTIaluq6xoNDr7VEO8PPS2FeBiILP_CGOqB3XdSC-oIUljqCGPsh6Ar9U=", "5cnPvheAsV5bTdIu0Wcx6sSFUpCJe6zTX-zPM-g3vW4=")
	if err != nil {
		t.Fatal(err)
	}
	transport := &outboxTransport{}
	transport.status.Store(503)
	app.Push = &integrations.PushSender{VAPID: vapid, Resolver: &countingDNS{}, Client: &http.Client{Transport: transport}}
	message, err := app.DB.CreateMessage(ctx, owner.ID, room.ID, "outbox-replay", "<p>replay</p>", "replay")
	if err != nil {
		t.Fatal(err)
	}
	app.Push.Resolver = unavailablePushDNS{}
	if err = app.drainPushOutbox(ctx); err == nil {
		t.Fatal("DNS failure was acknowledged")
	}
	if due, err := app.DB.DuePush(ctx, nil, 10); err != nil || len(due) != 0 {
		t.Fatalf("failed intent did not back off: %v %v", due, err)
	}
	if _, err = app.DB.Write.Exec("UPDATE campfire_push_outbox SET retry_at=0; UPDATE campfire_push_progress SET attempted_at=0 WHERE completed=0"); err != nil {
		t.Fatal(err)
	}
	app.Push.Resolver = &countingDNS{}
	if err = app.drainPushOutbox(ctx); err == nil {
		t.Fatal("delivery failure was acknowledged")
	}
	ids, err := app.DB.PendingPush(ctx, nil, 10)
	if err != nil || len(ids) != 1 || ids[0] != message.ID {
		t.Fatalf("retry intent: %v %v", ids, err)
	}
	transport.status.Store(201)
	if _, err = app.DB.Write.Exec("UPDATE campfire_push_outbox SET retry_at=0; UPDATE campfire_push_progress SET attempted_at=0 WHERE completed=0"); err != nil {
		t.Fatal(err)
	}
	// No messageCreated wake: a fresh runner must discover committed intent.
	app.Jobs = jobs.New(1, "push")
	app.Jobs.RegisterDurable("push", app.drainPushOutbox)
	app.Jobs.Close(3 * time.Second)
	ids, err = app.DB.PendingPush(ctx, nil, 10)
	if err != nil || len(ids) != 0 {
		t.Fatalf("startup replay: %v %v", ids, err)
	}
	if transport.calls.Load() != 2 {
		t.Fatalf("attempts=%d", transport.calls.Load())
	}
	if err = app.drainPushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if transport.calls.Load() != 2 {
		t.Fatal("acknowledged intent replayed")
	}
}
