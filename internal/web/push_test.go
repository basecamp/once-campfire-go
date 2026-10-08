package web

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/integrations"
)

type publicPushDNS struct{}

func (publicPushDNS) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}
func TestPushSubscriptions(t *testing.T) {
	app, server, cookie, user := testApp(t)
	app.Push.Resolver = publicPushDNS{}
	body := `{"endpoint":"https://fcm.googleapis.com/test","p256dh_key":"key","auth_key":"auth"}`
	for range 2 {
		response, data := perform(t, server, "POST", pushPath, "application/json", strings.NewReader(body), cookie)
		if response.StatusCode != 200 {
			t.Fatalf("create: %s %s", response.Status, data)
		}
	}
	list, err := app.DB.PushSubscriptions(context.Background(), user.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("duplicate subscription: %v %v", list, err)
	}
	response, data := perform(t, server, "GET", pushPath, "", nil, cookie)
	if response.StatusCode != 200 || !strings.Contains(string(data), "https://fcm.googleapis.com/test") {
		t.Fatalf("index: %s %s", response.Status, data)
	}
	response, _ = perform(t, server, "POST", pushPath, "application/json", strings.NewReader(`{"endpoint":"https://localhost/test"}`), cookie)
	if response.StatusCode != 422 {
		t.Fatal(response.Status)
	}
	response, _ = perform(t, server, "POST", fmt.Sprintf("%s/%d/test_notifications", pushPath, list[0].ID), "", nil, cookie)
	if response.StatusCode != 500 {
		t.Fatalf("unconfigured Web Push: %s", response.Status)
	}
	response, _ = perform(t, server, "DELETE", fmt.Sprintf("%s/%d", pushPath, list[0].ID), "", nil, cookie)
	if response.StatusCode != 302 || response.Header.Get("Location") != server.URL+pushPath {
		t.Fatal(response.Status, response.Header)
	}
	list, err = app.DB.PushSubscriptions(context.Background(), user.ID)
	if err != nil || len(list) != 0 {
		t.Fatalf("delete: %v %v", list, err)
	}
}

// countingDNS records lookups so the test can observe the push job running
// off the posting request.
type countingDNS struct {
	calls atomic.Int64
}

func (d *countingDNS) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	d.calls.Add(1)
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

// TestPushDeliveryRunsAsJob pins the reference's Room::PushMessageJob split:
// a message post with Web Push configured enqueues the delivery and responds
// immediately; the recipient/badge/delivery chain runs later on the job
// worker (its first observable step is the endpoint resolve).
func TestPushDeliveryRunsAsJob(t *testing.T) {
	app, server, cookie, user := testApp(t)
	vapid, err := integrations.NewVAPID("mailto:test@test", "BCx3gcVTvPQLdipX32-QcGJFqD_JISKTIaluq6xoNDr7VEO8PPS2FeBiILP_CGOqB3XdSC-oIUljqCGPsh6Ar9U=", "5cnPvheAsV5bTdIu0Wcx6sSFUpCJe6zTX-zPM-g3vW4=")
	if err != nil {
		t.Fatal(err)
	}
	dns := &countingDNS{}
	app.Push = &integrations.PushSender{Resolver: dns, VAPID: vapid}
	// The recipient query excludes the creator, so create a second member
	// with a subscription (the audited helpers keep the registries honest).
	_, err = app.DB.CreateUser(context.Background(), "Push Recipient", "recipient@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatal("no rooms for user")
	}
	recipient, err := app.DB.UserByEmail(context.Background(), "recipient@test")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.SetInvolvement(context.Background(), recipient.ID, rooms[0].ID, "everything"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Write.ExecContext(context.Background(),
		"INSERT INTO push_subscriptions(user_id,endpoint,p256dh_key,auth_key,user_agent,created_at,updated_at) VALUES(?,?,?,?,?,'now','now')",
		recipient.ID, "https://fcm.googleapis.com/fcm/send/test-endpoint", "key", "auth", "test"); err != nil {
		t.Fatal(err)
	}
	before := dns.calls.Load()
	response, data := perform(t, server, "POST", fmt.Sprintf("/rooms/%d/messages", rooms[0].ID),
		"application/x-www-form-urlencoded",
		strings.NewReader("message%5Bbody%5D=push%20me"), cookie)
	if response.StatusCode != 200 {
		t.Fatalf("post: %s %s", response.Status, data)
	}
	// The response must not depend on the push chain: the resolve (the job's
	// first step) counts up only after the response is back.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if dns.calls.Load() > before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("push job never ran its delivery after the posting response")
}
