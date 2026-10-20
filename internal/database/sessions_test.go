package database

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSessionCreationRejectsInactiveUser(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	u, err := d.Setup(ctx, "User", "user@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Write.ExecContext(ctx, "UPDATE users SET status=2 WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	if token, err := d.StartSession(ctx, u.ID, "browser", "203.0.113.10"); !errors.Is(err, sql.ErrNoRows) || token != "" {
		t.Fatalf("inactive session created: %q %v", token, err)
	}
	var count int
	if err = d.Read.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestAuthenticationRefreshesActivityOnce(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	d.Now = func() time.Time { return now }
	u, err := d.Setup(ctx, "User", "user@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	token, err := d.StartSession(ctx, u.ID, "original", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	u.User, err = d.User(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := d.ResponseVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, refreshed, err := d.AuthenticateSession(ctx, token, "new", "127.0.0.2")
	if err != nil || got != u.User || refreshed {
		t.Fatal(got, refreshed, err)
	}
	after, err := d.ResponseVersion(ctx)
	if err != nil || after != before {
		t.Fatalf("ordinary authentication wrote: %d -> %d, %v", before, after, err)
	}
	now = now.Add(2 * time.Hour)
	results := make(chan bool, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			got, refreshed, err := d.AuthenticateSession(ctx, token, "new", "127.0.0.2")
			if err != nil || got.ID != u.ID {
				t.Errorf("authentication: %v, %v", got, err)
			}
			results <- refreshed
		})
	}
	workers.Wait()
	close(results)
	refreshes := 0
	for refreshed := range results {
		if refreshed {
			refreshes++
		}
	}
	if refreshes != 1 {
		t.Fatalf("hourly refreshes: %d", refreshes)
	}
	var active time.Time
	var agent, ip string
	if err := d.Read.QueryRow("SELECT last_active_at,user_agent,ip_address FROM sessions WHERE token=?", token).Scan(timestamp{&active}, &agent, &ip); err != nil || !active.Equal(now) || agent != "new" || ip != "127.0.0.2" {
		t.Fatal(active, agent, ip, err)
	}
	if _, err := d.Write.Exec("UPDATE users SET status=2 WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	if _, refreshed, err := d.AuthenticateSession(ctx, token, "blocked", ip); !errors.Is(err, sql.ErrNoRows) || refreshed {
		t.Fatal("banned user authenticated", refreshed, err)
	}
}
