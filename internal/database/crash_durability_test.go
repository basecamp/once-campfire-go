package database

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type crashCommitConn struct{ laneConn }

func (c crashCommitConn) begin(ctx context.Context) (laneTx, error) {
	tx, err := c.laneConn.begin(ctx)
	return &crashCommitTx{laneTx: tx}, err
}

type crashCommitTx struct {
	laneTx
	message bool
}

func (t *crashCommitTx) InsertMessage(ctx context.Context, client string, user, room int64, stamp string) (int64, error) {
	t.message = true
	return t.laneTx.InsertMessage(ctx, client, user, room, stamp)
}
func (t *crashCommitTx) commit() error {
	err := t.laneTx.commit()
	if err == nil && t.message {
		os.Exit(91)
	}
	return err
}

func TestMessageCrashDurability(t *testing.T) {
	if path := os.Getenv("CAMPFIRE_CRASH_DB"); path != "" {
		ctx := context.Background()
		d, err := Open(path, 2)
		if err != nil {
			t.Fatal(err)
		}
		owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
		if err != nil {
			t.Fatal(err)
		}
		observer, err := d.CreateUser(ctx, "Observer", "observer@test", "digest", "", 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		room, err := d.CreateRoom(ctx, owner.ID, "Rooms::Open", "Crash", []int64{owner.ID, observer.ID})
		if err != nil {
			t.Fatal(err)
		}
		if os.Getenv("CAMPFIRE_CRASH_BOUNDARY") == "primary" {
			d.writer.conn = crashCommitConn{d.writer.conn}
		}
		release := d.LaneBlocker()
		for {
			d.writer.mu.Lock()
			busy := d.writer.busy
			d.writer.mu.Unlock()
			if busy {
				break
			}
			time.Sleep(time.Millisecond)
		}
		result := make(chan error, 1)
		go func() {
			_, err := d.CreateMessage(ctx, owner.ID, room.ID, "crash-message", "<p>durablebody</p>", "durablebody")
			result <- err
		}()
		for len(d.writer.jobs) == 0 {
			time.Sleep(time.Millisecond)
		}
		release()
		if err = <-result; err != nil {
			t.Fatal(err)
		}
		os.Exit(91) // unread committed; notification has not run
	}
	for _, boundary := range []string{"primary", "after-unread"} {
		t.Run(boundary, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "crash.sqlite3")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMessageCrashDurability$", "-test.timeout=10s")
			cmd.Env = append(os.Environ(), "CAMPFIRE_CRASH_DB="+path, "CAMPFIRE_CRASH_BOUNDARY="+boundary, "CAMPFIRE_WRITE_QUEUE=on")
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 91 {
				t.Fatalf("child: %v %s", err, output)
			}
			d, err := Open(path, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			var body string
			if err = d.Read.QueryRow("SELECT body FROM action_text_rich_texts WHERE record_id=(SELECT id FROM messages WHERE client_message_id='crash-message')").Scan(&body); err != nil || body != "<p>durablebody</p>" {
				t.Fatalf("body=%q err=%v", body, err)
			}
			var count int
			if err = d.Read.QueryRow("SELECT count(*) FROM message_search_index WHERE message_search_index MATCH 'durablebody'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("FTS=%d err=%v", count, err)
			}
			if err = d.Read.QueryRow("SELECT count(*) FROM memberships m JOIN users u ON u.id=m.user_id WHERE u.email_address='observer@test' AND m.unread_at IS NOT NULL").Scan(&count); err != nil || count != 1 {
				t.Errorf("unread=%d err=%v", count, err)
			}
			if err = d.Read.QueryRow("SELECT count(*) FROM campfire_push_outbox").Scan(&count); err != nil || count != 1 {
				t.Errorf("replayable notifications=%d err=%v", count, err)
			}
		})
	}
}
