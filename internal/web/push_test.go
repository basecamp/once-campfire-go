package web

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
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
	list, err := app.DB.PushSubscriptionsForUser(context.Background(), user.ID)
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
	list, err = app.DB.PushSubscriptionsForUser(context.Background(), user.ID)
	if err != nil || len(list) != 0 {
		t.Fatalf("delete: %v %v", list, err)
	}
}
