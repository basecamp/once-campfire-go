package web

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"github.com/mattn/go-sqlite3"
)

// The provider receipt is outside SQLite. The child exits without defers, once
// after acceptance and once inside the second progress INSERT of a three-row batch.
func TestPushOutboxCrashReplay(t *testing.T) {
	if path := os.Getenv("CAMPFIRE_PUSH_CRASH_DB"); path != "" {
		db, err := database.Open(path, 2)
		if err != nil {
			t.Fatal(err)
		}
		secrets, err := rails.NewSecrets("http-tests")
		if err != nil {
			t.Fatal(err)
		}
		vapid, err := integrations.NewVAPID("mailto:test@test", "BCx3gcVTvPQLdipX32-QcGJFqD_JISKTIaluq6xoNDr7VEO8PPS2FeBiILP_CGOqB3XdSC-oIUljqCGPsh6Ar9U=", "5cnPvheAsV5bTdIu0Wcx6sSFUpCJe6zTX-zPM-g3vW4=")
		if err != nil {
			t.Fatal(err)
		}
		boundary := os.Getenv("CAMPFIRE_PUSH_CRASH_BOUNDARY")
		receipt := os.Getenv("CAMPFIRE_PUSH_CRASH_RECEIPT")
		app := &Server{DB: db, Secrets: secrets, Storage: storage.New(db, secrets, filepath.Dir(path))}
		app.Push = &integrations.PushSender{VAPID: vapid, Resolver: &countingDNS{}, Client: &http.Client{Transport: progressTransport(func(r *http.Request) (*http.Response, error) {
			f, err := os.OpenFile(receipt, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.WriteString(r.URL.Path + "\n"); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			if boundary == "accepted" {
				os.Exit(91)
			}
			return pushResponse(201)
		})}}
		if boundary == "progress-insert" {
			conn, err := db.Write.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = conn.Raw(func(raw any) error {
				inserts := 0
				raw.(*sqlite3.SQLiteConn).RegisterUpdateHook(func(op int, schema, table string, row int64) {
					if table == "campfire_push_progress" && op == sqlite3.SQLITE_INSERT {
						inserts++
						if inserts != 2 {
							return
						}
						if err := os.WriteFile(receipt+".inserted", []byte("second progress row inserted, transaction not committed"), 0600); err != nil {
							panic(err)
						}
						os.Exit(91)
					}
				})
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
		}
		if err = app.drainPushOutbox(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Fatal("crash boundary not reached")
	}
	for _, boundary := range []string{"accepted", "progress-insert"} {
		t.Run(boundary, func(t *testing.T) {
			app, _ := progressApp(t, 3)
			var path string
			if err := app.DB.Read.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
				t.Fatal(err)
			}
			receipt := filepath.Join(t.TempDir(), "provider-receipts")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPushOutboxCrashReplay$", "-test.timeout=15s")
			cmd.Env = append(os.Environ(), "CAMPFIRE_PUSH_CRASH_DB="+path, "CAMPFIRE_PUSH_CRASH_BOUNDARY="+boundary, "CAMPFIRE_PUSH_CRASH_RECEIPT="+receipt)
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 91 {
				t.Fatalf("child: %v %s", err, output)
			}
			accepted, err := os.ReadFile(receipt)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if boundary == "progress-insert" {
				want = 3
				if _, err := os.Stat(receipt + ".inserted"); err != nil {
					t.Fatal(err)
				}
			}
			if got := strings.Count(string(accepted), "\n"); got != want {
				t.Fatalf("remote acceptances=%d, want %d", got, want)
			}
			pending, err := app.DB.PendingPush(context.Background(), nil, 128)
			if err != nil || len(pending) != 1 {
				t.Fatalf("lost intent after %s crash: %v %v", boundary, pending, err)
			}
			var count int
			if err = app.DB.Read.QueryRow("SELECT count(*) FROM campfire_push_progress").Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial transaction survived: progress=%d err=%v", count, err)
			}
			calls := map[string]int{}
			app.Push.Client.Transport = progressTransport(func(r *http.Request) (*http.Response, error) { calls[r.URL.Path]++; return pushResponse(201) })
			if err = app.drainPushOutbox(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err = app.drainPushOutbox(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 3 || calls["/fcm/send/0"] != 1 || calls["/fcm/send/1"] != 1 || calls["/fcm/send/2"] != 1 {
				t.Fatalf("replay deliveries=%v", calls)
			}
			pending, err = app.DB.PendingPush(context.Background(), nil, 128)
			if err != nil || len(pending) != 0 {
				t.Fatalf("replay not acknowledged: %v %v", pending, err)
			}
			t.Logf("%s: %d remote acceptances before crash; all three recipients replayed; partial progress rolled back", boundary, want)
		})
	}
}
