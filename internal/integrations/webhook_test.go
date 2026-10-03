package integrations

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWebhookOracle(t *testing.T) {
	var cases []struct {
		Name, Body, URL string
		Status          int
		Headers         [][2]string
		Body64          string `json:"body_b64"`
		Gzip            bool
		Delay           int
	}
	var expected []struct {
		Status *int
		Error  string
		Reply  *struct {
			Text64     *string `json:"text_b64"`
			Attachment *struct {
				Filename    string
				ContentType string `json:"content_type"`
				Body64      string `json:"body_b64"`
			}
		}
	}
	for name, target := range map[string]any{"webhook_cases.json": &cases, "webhook_expected.json": &expected} {
		raw, err := os.ReadFile("../../reference/crates/campfire/src/integrations/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload, _ := io.ReadAll(r.Body)
				if string(payload) != `{"message":"hi"}` || r.Header.Get("User-Agent") != "Ruby" || r.Header.Get("Accept-Encoding") != "gzip;q=1.0,deflate;q=0.6,identity;q=0.3" {
					t.Errorf("request mismatch: %v %s", r.Header, payload)
				}
				if c.Delay > 0 {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(time.Duration(c.Delay) * time.Second):
					}
				}
				for _, h := range c.Headers {
					w.Header().Add(h[0], h[1])
				}
				if _, ok := w.Header()["Content-Type"]; !ok {
					w.Header()["Content-Type"] = nil
				}
				body := []byte(c.Body)
				if c.Body64 != "" {
					body, _ = base64.StdEncoding.DecodeString(c.Body64)
				}
				if c.Gzip {
					w.Header().Set("Content-Encoding", "gzip")
					w.WriteHeader(c.Status)
					z := gzip.NewWriter(w)
					z.Write(body)
					z.Close()
				} else {
					w.WriteHeader(c.Status)
					w.Write(body)
				}
			}))
			defer server.Close()
			endpoint := server.URL + "/" + c.Name
			if c.URL != "" {
				endpoint = c.URL
			}
			got, err := NewWebhookClient().Deliver(context.Background(), endpoint, []byte(`{"message":"hi"}`))
			want := expected[i]
			if want.Error != "" {
				if err == nil {
					t.Fatal("expected error", want.Error)
				}
				if want.Error == "Mime::Type::InvalidMimeType" && !errors.Is(err, ErrWebhookMIME) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want.Status == nil {
				if got.Status != 0 {
					t.Fatal("timeout status", got.Status)
				}
			} else if got.Status != *want.Status {
				t.Fatal("status", got.Status, *want.Status)
			}
			if want.Reply == nil {
				if got.Text != nil || got.Filename != "" {
					t.Fatal("unexpected reply")
				}
				return
			}
			if text := want.Reply.Text64; text != nil {
				raw, _ := base64.StdEncoding.DecodeString(*text)
				if got.Text == nil || *got.Text != strings.ToValidUTF8(string(raw), "�") {
					t.Fatalf("text mismatch: %v", got.Text)
				}
			}
			if attachment := want.Reply.Attachment; attachment != nil {
				raw, _ := base64.StdEncoding.DecodeString(attachment.Body64)
				if got.Filename != attachment.Filename || got.ContentType != attachment.ContentType || string(got.Attachment) != string(raw) {
					t.Fatalf("attachment mismatch: %s %s (%d bytes)", got.Filename, got.ContentType, len(got.Attachment))
				}
			}
		})
	}
}
