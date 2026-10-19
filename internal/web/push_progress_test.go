package web

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/mattn/go-sqlite3"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type progressTransport func(*http.Request) (*http.Response, error)

func (f progressTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func pushResponse(status int) (*http.Response, error) {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
}
func progressApp(t *testing.T, recipients int) (*Server, database.Message) {
	t.Helper()
	app, _, _, owner := testApp(t)
	app.Jobs.Close(time.Second)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body := "<p>progress</p>"
	for i := 0; i < recipients; i++ {
		u, err := app.DB.CreateUser(ctx, "recipient", fmt.Sprintf("progress%d@test", i), "digest", "", 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		involvement := "everything"
		if i > 0 {
			involvement = "mentions"
			body += fmt.Sprintf(`<action-text-attachment sgid="%s"></action-text-attachment>`, app.Secrets.SGID(fmt.Sprintf("gid://campfire/User/%d?expires_in", u.ID), "attachable", time.Time{}))
		}
		if err = app.DB.SetInvolvement(ctx, u.ID, rooms[0].ID, involvement); err != nil {
			t.Fatal(err)
		}
		_, err = app.DB.Write.Exec(`INSERT INTO push_subscriptions(user_id,endpoint,p256dh_key,auth_key,user_agent,created_at,updated_at) VALUES(?,?,?,?,?,'now','now')`, u.ID, fmt.Sprintf("https://fcm.googleapis.com/fcm/send/%d", i), base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(make([]byte, 16)), "test")
		if err != nil {
			t.Fatal(err)
		}
	}
	vapid, err := integrations.NewVAPID("mailto:test@test", "BCx3gcVTvPQLdipX32-QcGJFqD_JISKTIaluq6xoNDr7VEO8PPS2FeBiILP_CGOqB3XdSC-oIUljqCGPsh6Ar9U=", "5cnPvheAsV5bTdIu0Wcx6sSFUpCJe6zTX-zPM-g3vW4=")
	if err != nil {
		t.Fatal(err)
	}
	app.Push = &integrations.PushSender{VAPID: vapid, Resolver: &countingDNS{}, Client: &http.Client{}}
	m, err := app.DB.CreateMessage(ctx, owner.ID, rooms[0].ID, "progress", body, "progress")
	if err != nil {
		t.Fatal(err)
	}
	return app, m
}
func TestPushOutboxRecipientProgress(t *testing.T) {
	t.Run("failure isolation", func(t *testing.T) {
		app, _ := progressApp(t, 2)
		calls := map[string]int{}
		app.Push.Client.Transport = progressTransport(func(r *http.Request) (*http.Response, error) {
			calls[r.URL.Path]++
			if strings.HasSuffix(r.URL.Path, "/0") {
				return pushResponse(503)
			}
			return pushResponse(201)
		})
		for i := 0; i < 3; i++ {
			_, _ = app.DB.Write.Exec("UPDATE campfire_push_outbox SET retry_at=0")
			_ = app.drainPushOutbox(context.Background())
		}
		if got := calls["/fcm/send/1"]; got != 1 {
			t.Fatalf("healthy deliveries=%d, want exactly 1", got)
		}
	})
	t.Run("budget resumes suffix", func(t *testing.T) {
		app, _ := progressApp(t, 3)
		calls := map[string]int{}
		for i := 0; i < 4; i++ {
			ctx, cancel := context.WithCancel(context.Background())
			app.Push.Client.Transport = progressTransport(func(r *http.Request) (*http.Response, error) {
				calls[r.URL.Path]++
				cancel()
				return pushResponse(201)
			})
			_, _ = app.DB.Write.Exec("UPDATE campfire_push_outbox SET retry_at=0")
			_ = app.drainPushOutbox(ctx)
			cancel()
		}
		for i := 0; i < 3; i++ {
			if got := calls[fmt.Sprintf("/fcm/send/%d", i)]; got != 1 {
				t.Errorf("recipient %d deliveries=%d, want 1", i, got)
			}
		}
	})
}
func TestPushOutboxExpiredScanLeavesUntouchedDue(t *testing.T) {
	app, first := progressApp(t, 1)
	second, err := app.DB.CreateMessage(context.Background(), first.CreatorID, first.RoomID, "second", "second", "second")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.Push.Client.Transport = progressTransport(func(r *http.Request) (*http.Response, error) { cancel(); return nil, context.DeadlineExceeded })
	_ = app.drainPushOutbox(ctx)
	var retry int64
	if err := app.DB.Read.QueryRow("SELECT retry_at FROM campfire_push_outbox WHERE message_id=?", second.ID).Scan(&retry); err != nil {
		t.Fatal(err)
	}
	if retry != 0 {
		t.Fatalf("unattempted message deferred until %d", retry)
	}
}

func TestPushOutboxRechecksEligibility(t *testing.T) {
	app, message := progressApp(t, 2)
	calls := 0
	app.Push.Client.Transport = progressTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if _, err := app.DB.Write.Exec("UPDATE memberships SET involvement='nothing' WHERE room_id=?", message.RoomID); err != nil {
			t.Fatal(err)
		}
		return pushResponse(201)
	})
	if err := app.drainPushOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("deliveries=%d, want 1 after eligibility changes", calls)
	}
}
func TestPushOutboxBatchesAcknowledgements(t *testing.T) {
	app, first := progressApp(t, 0)
	app.Push.VAPID = nil
	for i := 1; i < 128; i++ {
		if _, err := app.DB.CreateMessage(context.Background(), first.CreatorID, first.RoomID, fmt.Sprint(i), "batch", "batch"); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := app.DB.Write.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	commits := 0
	if err = conn.Raw(func(raw any) error {
		raw.(*sqlite3.SQLiteConn).RegisterCommitHook(func() int { commits++; return 0 })
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	defer func() {
		c, err := app.DB.Write.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.Raw(func(raw any) error { raw.(*sqlite3.SQLiteConn).RegisterCommitHook(nil); return nil })
	}()
	if err = app.drainPushOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("128 messages: %d acknowledgement commits", commits)
	if commits != 1 {
		t.Fatalf("acknowledgement commits=%d, want 1", commits)
	}
}
