package web

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/url"
	"strings"
	"testing"
)

// Registration must use the selected parameter root, not a second JSON parse or
// flattened body/query leaf merge. Rejected requests must not register credentials.
func TestPushSubscriptionParametersKeepSourceAndScope(t *testing.T) {
	app, server, cookie, user := testApp(t)
	app.Push.Resolver = publicPushDNS{}
	valid := `{"endpoint":"https://fcm.googleapis.com/scope-test","p256dh_key":"body-key","auth_key":"body-auth"}`
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	for key, value := range map[string]string{"endpoint": "https://fcm.googleapis.com/scope-test", "p256dh_key": "body-key"} {
		if err := writer.WriteField("push_subscription["+key+"]", value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, query, media, body string
		status                   int
	}{
		{"empty nested root", "", "application/json", `{"endpoint":"https://fcm.googleapis.com/scope-test","push_subscription":{}}`, 400},
		{"outer attributes do not fill nested root", "", "application/json", `{"endpoint":"https://fcm.googleapis.com/scope-test","push_subscription":{"auth_key":"nested-auth"}}`, 422},
		{"query clears JSON root", "push_subscription=", "application/json", valid, 400},
		{"query replaces nested JSON root", "push_subscription[auth_key]=query-auth", "application/json", `{"push_subscription":` + valid + `}`, 422},
		{"query prevents JSON wrapping", "push_subscription[auth_key]=query-auth", "application/json", valid, 422},
		{"query replaces form root", "push_subscription[auth_key]=query-auth", "application/x-www-form-urlencoded", url.Values{"push_subscription[endpoint]": {"https://fcm.googleapis.com/scope-test"}, "push_subscription[p256dh_key]": {"body-key"}}.Encode(), 422},
		{"query replaces multipart root", "push_subscription[auth_key]=query-auth", writer.FormDataContentType(), multipartBody.String(), 422},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := pushPath
			if test.query != "" {
				path += "?" + test.query
			}
			response, body := perform(t, server, "POST", path, test.media, strings.NewReader(test.body), cookie)
			if response.StatusCode != test.status {
				t.Errorf("status %d, want %d: %s", response.StatusCode, test.status, body)
			}
			list, err := app.DB.PushSubscriptions(context.Background(), user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 0 {
				t.Errorf("rejected registration stored credentials: %+v", list)
				for _, subscription := range list {
					if err := app.DB.DeletePushSubscription(context.Background(), user.ID, subscription.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}

	// MIME normalization and permitted scalar casts must retain credentials, while
	// an unrelated top-level query field must not enter the JSON wrapper.
	response, body := perform(t, server, "POST", pushPath+"?endpoint=https://localhost/not-selected", "Application/JSON; charset=utf-8", strings.NewReader(`{"endpoint":"https://fcm.googleapis.com/scalar-test","p256dh_key":7,"auth_key":true}`), cookie)
	if response.StatusCode != 200 {
		t.Fatalf("typed registration: %s %s", response.Status, body)
	}
	list, err := app.DB.PushSubscriptions(context.Background(), user.ID)
	if err != nil || len(list) != 1 || list[0].Endpoint != "https://fcm.googleapis.com/scalar-test" || list[0].Key != "7" || list[0].Auth != "true" {
		t.Fatalf("selected typed credentials: %+v %v", list, err)
	}
}
