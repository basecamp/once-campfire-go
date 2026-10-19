package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
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

// countingDNS is shared by the outbox and crash-recovery tests.
type countingDNS struct {
	calls atomic.Int64
}

func (d *countingDNS) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	d.calls.Add(1)
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

type blockingPushDNS struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *blockingPushDNS) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	d.once.Do(func() { close(d.entered) })
	select {
	case <-d.release:
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestPushDeliveryRunsAsJob pins the reference's Room::PushMessageJob split:
// a message post with Web Push configured enqueues the delivery and responds
// without waiting for the job worker's recipient/badge/delivery chain.
// Resolution may start before the response, but must not hold up the POST.
func TestPushDeliveryRunsAsJob(t *testing.T) {
	app, server, cookie, user := testApp(t)
	vapid, err := integrations.NewVAPID("mailto:test@test", "BCx3gcVTvPQLdipX32-QcGJFqD_JISKTIaluq6xoNDr7VEO8PPS2FeBiILP_CGOqB3XdSC-oIUljqCGPsh6Ar9U=", "5cnPvheAsV5bTdIu0Wcx6sSFUpCJe6zTX-zPM-g3vW4=")
	if err != nil {
		t.Fatal(err)
	}
	dns := &blockingPushDNS{entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(dns.release) })
	t.Cleanup(release)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/rooms/%d/messages", server.URL, rooms[0].ID),
		strings.NewReader("message%5Bbody%5D=push%20me"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", server.URL)
	req.AddCookie(cookie)
	type postResult struct {
		status int
		body   []byte
		err    error
	}
	posted := make(chan postResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		response, err := server.Client().Do(req)
		if err != nil {
			posted <- postResult{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		posted <- postResult{status: response.StatusCode, body: body, err: err}
	}()
	t.Cleanup(func() {
		release()
		cancel()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("posting request did not stop during cleanup")
		}
	})
	select {
	case <-dns.entered:
	case <-ctx.Done():
		t.Fatal("push job never entered endpoint resolution")
	}
	select {
	case result := <-posted:
		if result.err != nil || result.status != http.StatusOK {
			t.Fatalf("post: status %d, body %s, error %v", result.status, result.body, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("posting response waited for blocked push delivery")
	}
	release()
}
