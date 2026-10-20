package integrations

import "testing"

// Provider payloads must retain Rails' field order, nullable HTML, integer
// precision and escaping without applying cookie/verifier encoding policy.
func TestNotificationPayloadEncoding(t *testing.T) {
	got := NotificationJSON("<>&\u2028\u2029", "quote\" slash\\ literal\\u2028\n\x01\xff", "/rooms/9", 9223372036854775807)
	want := `{"title":"<>&` + "\u2028\u2029" + `","options":{"body":"quote\" slash\\ literal\\u2028\n\u0001�","icon":"/account/logo","data":{"path":"/rooms/9","badge":9223372036854775807}}}`
	if string(got) != want {
		t.Fatalf("push JSON:\n%s\nwant:\n%s", got, want)
	}
}

func TestWebhookPayloadEncoding(t *testing.T) {
	html := "<p>&\u2028\u2029</p>"
	for _, c := range []struct {
		name string
		html *string
		want string
	}{
		{"absent", nil, `null`},
		{"empty", new(""), `""`},
		{"escaped", &html, `"\u003cp\u003e\u0026` + "\u2028\u2029" + `\u003c/p\u003e"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := (WebhookContent{CreatorID: 9223372036854775807, Creator: "<>&\xff", RoomID: 9, MessageID: 11, BotKey: "1-key", Plain: "literal\\u2029\n", HTML: c.html}).JSON()
			if err != nil {
				t.Fatal(err)
			}
			want := `{"user":{"id":9223372036854775807,"name":"\u003c\u003e\u0026�"},"room":{"id":9,"name":null,"path":"/rooms/9/1-key/messages"},"message":{"id":11,"body":{"html":` + c.want + `,"plain":"literal\\u2029\n"},"path":"/rooms/9/@11"}}`
			if string(got) != want {
				t.Fatalf("webhook JSON:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
