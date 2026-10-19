package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestPushOutboxLargeFailureCohortDoesNotStarve(t *testing.T) {
	app, first := progressApp(t, 129)
	ctx := context.Background()
	healthy, err := app.DB.CreateUser(ctx, "healthy", "healthy-round@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	room, err := app.DB.CreateRoom(ctx, first.CreatorID, "Rooms::Open", "Later", []int64{first.CreatorID, healthy.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.DB.SetInvolvement(ctx, healthy.ID, room.ID, "everything"); err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Write.Exec(`INSERT INTO push_subscriptions(user_id,endpoint,p256dh_key,auth_key,user_agent,created_at,updated_at) SELECT ?, 'https://fcm.googleapis.com/fcm/send/healthy',p256dh_key,auth_key,user_agent,created_at,updated_at FROM push_subscriptions LIMIT 1`, healthy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.CreateMessage(ctx, first.CreatorID, room.ID, "later", "later", "later"); err != nil {
		t.Fatal(err)
	}
	calls := make(map[string]int)
	app.Push.Client.Transport = progressTransport(func(r *http.Request) (*http.Response, error) {
		calls[r.URL.Path]++
		if strings.HasSuffix(r.URL.Path, "/healthy") {
			return pushResponse(201)
		}
		return pushResponse(503)
	})
	for i := 0; i < 4; i++ {
		_ = app.drainPushOutbox(ctx)
	}
	if got := calls["/fcm/send/healthy"]; got != 1 {
		t.Errorf("later message deliveries=%d, want 1", got)
	}
	for i := 0; i < 129; i++ {
		if got := calls[fmt.Sprintf("/fcm/send/%d", i)]; got != 1 {
			t.Errorf("recipient %d attempts=%d before backoff expires, want 1", i, got)
		}
	}
	due, err := app.DB.DuePush(ctx, nil, 128)
	if err != nil || len(due) != 0 {
		t.Errorf("failed cohort still immediately due: %v %v", due, err)
	}
	var retry int64
	if err = app.DB.Read.QueryRow("SELECT retry_at FROM campfire_push_outbox WHERE message_id=?", first.ID).Scan(&retry); err != nil {
		t.Fatal(err)
	}
	if retry <= time.Now().Unix() {
		t.Errorf("retry_at=%d is not in the future", retry)
	}
}

func TestPushEligibilityRefreshHasFixedQueryShape(t *testing.T) {
	app, _ := progressApp(t, 80)
	var path string
	if err := app.DB.Read.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	db, _, _, statements, err := database.OpenCounting(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := app.DB
	app.DB = db
	defer func() { app.DB = old }()
	app.Push.Client.Transport = progressTransport(func(*http.Request) (*http.Response, error) { return pushResponse(201) })
	if err = app.drainPushOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	for _, query := range statements() {
		if strings.Contains(query, "WHERE p.id=") {
			refreshes++
			if strings.Count(query, "?") > 6 {
				t.Fatalf("recipient refresh rebuilt mention placeholders: %d parameters", strings.Count(query, "?"))
			}
		}
	}
	if refreshes != 80 {
		t.Fatalf("refreshes=%d, want 80", refreshes)
	}
}

func TestPushProgressSubscriptionDeletionUsesIndex(t *testing.T) {
	app, _ := progressApp(t, 0)
	rows, err := app.DB.Read.Query("EXPLAIN QUERY PLAN DELETE FROM push_subscriptions WHERE id=?", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		if strings.Contains(detail, "SEARCH campfire_push_progress") && strings.Contains(detail, "(subscription_id=?)") {
			indexed = true
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("subscription cascade has no subscription-leading progress index")
	}
}
