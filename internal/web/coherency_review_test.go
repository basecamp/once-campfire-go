package web

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func foreignReviewDB(t *testing.T, app *Server) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+testDBPath(t, app.DB)+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func reviewContext(t *testing.T, app *Server) context.Context {
	t.Helper()
	v, err := app.DB.ResponseVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return context.WithValue(context.Background(), requestInfoKey{}, &requestInfo{databaseVersion: v})
}

func TestReviewReadCachesForeignWrites(t *testing.T) {
	for _, kind := range []string{"room", "account", "invitation", "original"} {
		t.Run(kind, func(t *testing.T) {
			app, user := testReadCacheApp(t)
			ctx := context.Background()
			rooms, err := app.DB.Rooms(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			id := rooms[0].ID
			foreign := foreignReviewDB(t, app)
			var read func(context.Context) (any, error)
			var query string
			var args []any
			switch kind {
			case "room":
				read = func(c context.Context) (any, error) { return app.roomRowCached(nil, c, user.ID, id) }
				query, args = "UPDATE rooms SET name='foreign room' WHERE id=?", []any{id}
			case "account":
				read = func(c context.Context) (any, error) { return app.accountCached(c) }
				query = "UPDATE accounts SET name='foreign account', custom_styles='body {color:red}'"
			case "invitation":
				for i := 0; i < 41; i++ {
					if _, err := app.DB.CreateMessage(ctx, user.ID, id, fmt.Sprint(i), "<p>x</p>", "x"); err != nil {
						t.Fatal(err)
					}
				}
				read = func(c context.Context) (any, error) { return app.invitationCached(nil, c, id) }
				query, args = "DELETE FROM messages WHERE room_id=?", []any{id}
			case "original":
				read = func(c context.Context) (any, error) { return app.originalRoomCached(c, user.ID) }
				query, args = "DELETE FROM memberships WHERE user_id=?", []any{user.ID}
			}
			before, err := read(reviewContext(t, app))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := foreign.Exec(query, args...); err != nil {
				t.Fatal(err)
			}
			got, gotErr := read(reviewContext(t, app))
			app.readCache = nil
			want, wantErr := read(reviewContext(t, app))
			if fmt.Sprint(got) != fmt.Sprint(want) || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
				t.Fatalf("stale %s: before=%v got=%v/%v oracle=%v/%v", kind, before, got, gotErr, want, wantErr)
			}
		})
	}
}

func TestReviewRefsForeignWrites(t *testing.T) {
	app, user := testReadCacheApp(t)
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	foreign := foreignReviewDB(t, app)
	read := func() []database.Message {
		refs, _, err := app.messageRefs(nil, reviewContext(t, app), room.ID, 0, "before", room.UpdatedAt)
		if err != nil {
			t.Fatal(err)
		}
		return refs
	}
	read() // Cache the empty window.
	if _, err := foreign.Exec("INSERT INTO messages (room_id,creator_id,client_message_id,created_at,updated_at) VALUES (?,?, 'foreign',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)", room.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	got := read()
	cache := app.refsCache
	app.refsCache = nil
	want := read()
	app.refsCache = cache
	if len(got) != len(want) {
		t.Errorf("insert: cached=%d oracle=%d", len(got), len(want))
	}
	// Warm a populated window independently so deletion is tested even if
	// the preceding empty-window regression failed.
	app.refsCache = newMessageRefsCache(8 << 20)
	read()
	if _, err := foreign.Exec("DELETE FROM messages WHERE room_id=?", room.ID); err != nil {
		t.Fatal(err)
	}
	got = read()
	app.refsCache = nil
	want = read()
	if len(got) != len(want) {
		t.Errorf("delete: cached=%d oracle=%d", len(got), len(want))
	}
}

func TestReviewForeignResponseParity(t *testing.T) {
	for _, kind := range []string{"room", "account", "insert", "delete", "invitation"} {
		t.Run(kind, func(t *testing.T) {
			app, on, _, cookie, user := testSingleApp(t)
			rooms, err := app.DB.Rooms(context.Background(), user.ID)
			if err != nil {
				t.Fatal(err)
			}
			room := rooms[0]
			if kind == "delete" || kind == "invitation" {
				n := 1
				if kind == "invitation" {
					n = 41
				}
				for i := 0; i < n; i++ {
					if _, err := app.DB.CreateMessage(context.Background(), user.ID, room.ID, fmt.Sprint(i), "<p>foreign parity</p>", "foreign parity"); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Setenv("CAMPFIRE_READ_CACHE", "off")
			t.Setenv("CAMPFIRE_MESSAGE_REFS_CACHE_MB", "0")
			t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
			oracle, err := New(app.DB, app.Secrets, false, testDBPath(t, app.DB), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(oracle.Close)
			off := httptest.NewServer(oracle)
			t.Cleanup(off.Close)
			path := fmt.Sprintf("/rooms/%d", room.ID)
			get := func(server *httptest.Server) (int, []byte) {
				req, _ := http.NewRequest("GET", server.URL+path, nil)
				req.Host = "campfire.test"
				req.AddCookie(cookie)
				res, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				body, err := io.ReadAll(res.Body)
				if err != nil {
					t.Fatal(err)
				}
				return res.StatusCode, body
			}
			get(on)
			foreign := foreignReviewDB(t, app)
			query := ""
			var args []any
			switch kind {
			case "room":
				query = "UPDATE rooms SET name='foreign renamed' WHERE id=?"
				args = []any{room.ID}
			case "account":
				query = "UPDATE accounts SET custom_styles='body { color: red; }'"
			case "insert":
				query = "INSERT INTO messages (room_id,creator_id,client_message_id,created_at,updated_at) VALUES (?,?, 'foreign',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)"
				args = []any{room.ID, user.ID}
			default:
				query = "DELETE FROM messages WHERE room_id=?"
				args = []any{room.ID}
			}
			if _, err := foreign.Exec(query, args...); err != nil {
				t.Fatal(err)
			}
			got, body := get(on)
			want, expected := get(off)
			if got != want || !bytes.Equal(body, expected) {
				t.Fatalf("foreign %s differs from cache-off: statuses %d/%d bytes %d/%d", kind, got, want, len(body), len(expected))
			}
		})
	}
}

func TestReviewRefsCommitDuringAdmission(t *testing.T) {
	app, user := testReadCacheApp(t)
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	ctx := reviewContext(t, app)
	refs, validator, err := app.messageRefs(nil, ctx, room.ID, 0, "before", room.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	// Capture the pre-read admission key, then interleave a foreign commit
	// before publishing that scan. No sleeps or scheduler-dependent timing.
	var key messageRefsKey
	app.refsCache.mu.Lock()
	for k := range app.refsCache.entries {
		key = k
	}
	app.refsCache.mu.Unlock()
	if _, err := foreignReviewDB(t, app).Exec("INSERT INTO messages (room_id,creator_id,client_message_id,created_at,updated_at) VALUES (?,?, 'during-admission',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)", room.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	app.refsCache.store(key, refs, validator)
	got, _, err := app.messageRefs(nil, reviewContext(t, app), room.ID, 0, "before", room.UpdatedAt)
	if err != nil || len(got) != 1 {
		t.Fatalf("old admission poisoned new generation: refs=%d err=%v", len(got), err)
	}
}

func TestReviewAuthorValidator(t *testing.T) {
	for _, cache := range []string{"0", "64"} {
		for _, pieces := range []string{"off", "on"} {
			for _, replay := range []string{"off", "on"} {
				t.Run(cache+"/"+pieces+"/"+replay, func(t *testing.T) {
					app, server, _, cookie, user := testSingleApp(t, "CAMPFIRE_RESPONSE_CACHE_MB="+cache, "CAMPFIRE_RECORDED_PIECES="+pieces, "CAMPFIRE_PRECOMPOSED_FRAMING="+replay)
					_, addr := serveLoop(t, publicReplayChain(app))
					rooms, err := app.DB.Rooms(context.Background(), user.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := app.DB.CreateMessage(context.Background(), user.ID, rooms[0].ID, "validator", "<p>hello</p>", "hello"); err != nil {
						t.Fatal(err)
					}
					get := func(etag string) (int, string, []byte) {
						req, _ := http.NewRequest("GET", "http://"+addr+fmt.Sprintf("/rooms/%d/messages", rooms[0].ID), nil)
						req.AddCookie(cookie)
						req.Header.Set("If-None-Match", etag)
						res, err := server.Client().Do(req)
						if err != nil {
							t.Fatal(err)
						}
						defer res.Body.Close()
						body, err := io.ReadAll(res.Body)
						if err != nil {
							t.Fatal(err)
						}
						return res.StatusCode, res.Header.Get("ETag"), body
					}
					status, old, body := get("")
					if status != 200 || old == "" {
						t.Fatalf("warm %d %q", status, old)
					}
					if _, err := foreignReviewDB(t, app).Exec("UPDATE users SET name='renamed author' WHERE id=?", user.ID); err != nil {
						t.Fatal(err)
					}
					status, next, changed := get(old)
					if status != 200 || bytes.Equal(body, changed) || old == next {
						t.Fatalf("rename: status=%d old=%q new=%q changed=%v", status, old, next, !bytes.Equal(body, changed))
					}
					if status, _, _ := get(next); status != 304 {
						t.Fatalf("fresh conditional: %d", status)
					}
				})
			}
		}
	}
}
