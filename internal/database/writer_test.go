package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// pragmaInt runs one scalar pragma on the given connection.
func pragmaInt(t *testing.T, db *sql.DB, query string) int64 {
	t.Helper()
	var value int64
	if err := db.QueryRow(query).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return value
}

func pragmaText(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	var value string
	if err := db.QueryRow(query).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return value
}

// TestWriteQueueDurabilityPragmas pins the settings the write lane relies on:
// WAL journal, synchronous=NORMAL, auto-checkpoint off on the writer,
// journal_size_limit untouched (the default -1) and the off-writer
// checkpointer present. The readers keep the same pragma set as before.
func TestWriteQueueDurabilityPragmas(t *testing.T) {
	d := testDB(t)
	if d.writer == nil {
		t.Fatal("write queue is off by default")
	}
	if d.checkpoints == nil {
		t.Fatal("no off-writer checkpointer")
	}
	if got := pragmaText(t, d.Write, "PRAGMA journal_mode"); got != "wal" {
		t.Errorf("writer journal_mode = %q, want wal", got)
	}
	if got := pragmaInt(t, d.Write, "PRAGMA synchronous"); got != 1 {
		t.Errorf("writer synchronous = %d, want NORMAL(1)", got)
	}
	if got := pragmaInt(t, d.Write, "PRAGMA wal_autocheckpoint"); got != 0 {
		t.Errorf("writer wal_autocheckpoint = %d, want 0 (checkpoints off the writer)", got)
	}
	if got := pragmaInt(t, d.Write, "PRAGMA journal_size_limit"); got != -1 {
		t.Errorf("writer journal_size_limit = %d, want -1 (unchanged)", got)
	}
	if got := pragmaInt(t, d.Write, "PRAGMA busy_timeout"); got != 5000 {
		t.Errorf("writer busy_timeout = %d, want 5000", got)
	}
	if got := pragmaText(t, d.Read.DB, "PRAGMA journal_mode"); got != "wal" {
		t.Errorf("reader journal_mode = %q, want wal", got)
	}
	if got := pragmaInt(t, d.Read.DB, "PRAGMA synchronous"); got != 1 {
		t.Errorf("reader synchronous = %d, want NORMAL(1)", got)
	}
	// A direct PASSIVE checkpoint on the checkpointer's connection must not
	// error while the writer is idle.
	d.checkpoints.passive()
}

// TestWriteQueueOffPath pins the A/B switch: CAMPFIRE_WRITE_QUEUE=off restores
// the per-request transaction path exactly, including the writer's
// auto-checkpoint (the setting the queue path switches off).
func TestWriteQueueOffPath(t *testing.T) {
	t.Setenv("CAMPFIRE_WRITE_QUEUE", "off")
	d := testDB(t)
	if d.writer != nil {
		t.Fatal("write queue still enabled with CAMPFIRE_WRITE_QUEUE=off")
	}
	if d.checkpoints != nil {
		t.Fatal("checkpointer running with the write queue off")
	}
	if got := pragmaInt(t, d.Write, "PRAGMA wal_autocheckpoint"); got == 0 {
		t.Fatal("wal_autocheckpoint is 0 on the direct path; it must stay at the default")
	}
	ctx := context.Background()
	u, err := d.Setup(ctx, "Direct", "direct@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, u.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatalf("setup rooms: %v %v", rooms, err)
	}
	m, err := d.CreateMessage(ctx, u.ID, rooms[0].ID, "", "<p>direct path</p>", "direct path")
	if err != nil {
		t.Fatal(err)
	}
	if m.Body != "<p>direct path</p>" || m.ID == 0 {
		t.Fatalf("message: %+v", m)
	}
	if hits, err := d.Search(ctx, u.ID, "direct"); err != nil || len(hits) != 1 {
		t.Fatalf("search after direct write: %v %v", hits, err)
	}
}

// TestWriteQueueInvalidFlagKeepsDefault pins the flag parsing: an
// unrecognised value keeps the queue on (like the web flags' default-on
// behaviour).
func TestWriteQueueInvalidFlagKeepsDefault(t *testing.T) {
	t.Setenv("CAMPFIRE_WRITE_QUEUE", "nope")
	d := testDB(t)
	if d.writer == nil {
		t.Fatal("unrecognised CAMPFIRE_WRITE_QUEUE turned the queue off")
	}
}

// seededWriterTest builds a database with an owner, an observer with a
// membership in a closed room, the closed room, and a stranger with no
// membership. Messages posted by the owner must bump unread_at for the
// observer; posts by the stranger must fail the membership check.
func seededWriterTest(t *testing.T) (d *DB, owner, observer, stranger, room int64) {
	t.Helper()
	d = testDB(t)
	ctx := context.Background()
	ownerUser, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	observerUser, err := d.CreateUser(ctx, "Observer", "observer@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	strangerUser, err := d.CreateUser(ctx, "Stranger", "stranger@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := d.CreateRoom(ctx, ownerUser.ID, "Rooms::Closed", "Write Lane", []int64{ownerUser.ID, observerUser.ID})
	if err != nil {
		t.Fatal(err)
	}
	return d, ownerUser.ID, observerUser.ID, strangerUser.ID, closed.ID
}

func (d *DB) readInt(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	err := d.Read.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

func (d *DB) readStamp(ctx context.Context, query string, args ...any) (time.Time, error) {
	var stamp time.Time
	err := d.Read.QueryRowContext(ctx, query, args...).Scan(timestamp{&stamp})
	return stamp, err
}

// TestWriteQueueConcurrentWriters runs many concurrent creators through the
// queue (-race) and asserts every request's own result, every message row,
// every search-index row and the room's unread bump: the exact surface the
// task's tests-first list names.
func TestWriteQueueConcurrentWriters(t *testing.T) {
	d, owner, observer, _, room := seededWriterTest(t)
	ctx := context.Background()
	const writers = 24
	const perWriter = 5
	start := make(chan struct{})
	var wg sync.WaitGroup
	messages := make([][]Message, writers)
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < perWriter; j++ {
				body := fmt.Sprintf("<p>writer %d message %d</p>", i, j)
				// One FTS token per message: SearchQuery splits query words
				// on '-' and '_', so hyphenated needles match every row that
				// shares a token.
				plain := fmt.Sprintf("wneedle%02d%02d", i, j)
				m, err := d.CreateMessage(ctx, owner, room, fmt.Sprintf("client-%d-%d", i, j), body, plain)
				if err != nil {
					errs[i] = err
					return
				}
				messages[i] = append(messages[i], m)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	// Every request's own result: ids, room, creator, client id, body.
	seen := make(map[int64]string, writers*perWriter)
	for i, list := range messages {
		if len(list) != perWriter {
			t.Fatalf("writer %d got %d results", i, len(list))
		}
		for j, m := range list {
			if m.RoomID != room || m.CreatorID != owner || m.Creator != "Owner" {
				t.Fatalf("writer %d msg %d: bad record %+v", i, j, m)
			}
			if want := fmt.Sprintf("client-%d-%d", i, j); m.ClientID != want {
				t.Fatalf("writer %d msg %d: client %q, want %q", i, j, m.ClientID, want)
			}
			if want := fmt.Sprintf("<p>writer %d message %d</p>", i, j); m.Body != want {
				t.Fatalf("writer %d msg %d: body %q, want %q", i, j, m.Body, want)
			}
			seen[m.ID] = m.ClientID
		}
	}
	if len(seen) != writers*perWriter {
		t.Fatalf("distinct ids = %d, want %d", len(seen), writers*perWriter)
	}

	// All rows persisted through the group commits.
	if n, err := d.readInt(ctx, "SELECT count(*) FROM messages WHERE room_id=?", room); err != nil || n != writers*perWriter {
		t.Fatalf("messages rows = %d %v, want %d", n, err, writers*perWriter)
	}
	// The search index holds every message body; a query for each needle
	// finds exactly its message.
	for i := 0; i < writers; i++ {
		for j := 0; j < perWriter; j++ {
			needle := fmt.Sprintf("wneedle%02d%02d", i, j)
			hits, err := d.Search(ctx, owner, needle)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) != 1 || hits[0].Body != fmt.Sprintf("<p>writer %d message %d</p>", i, j) {
				t.Fatalf("search %q: %v", needle, hits)
			}
		}
	}
	if n, err := d.readInt(ctx, "SELECT count(*) FROM message_search_index"); err != nil || n != writers*perWriter {
		t.Fatalf("search index rows = %d %v, want %d", n, err, writers*perWriter)
	}
	// The observer's membership was bumped by the last write (never
	// connected, involvement enabled); the bump is fresh.
	stamp, err := d.readStamp(ctx, "SELECT unread_at FROM memberships WHERE room_id=? AND user_id=?", room, observer)
	if err != nil {
		t.Fatal(err)
	}
	if age := time.Since(stamp); age < 0 || age > 5*time.Minute {
		t.Fatalf("observer unread_at not bumped by the batch: %v (%s ago)", stamp, age)
	}
}

// TestWriteQueueOrdering asserts ids strictly follow submission order: the
// queued tasks run in queue order inside one transaction, so LastInsertId
// must ascend with submission.
func TestWriteQueueOrdering(t *testing.T) {
	d, owner, _, _, room := seededWriterTest(t)
	ctx := context.Background()
	const total = 40
	ids := make([]int64, total)
	for i := 0; i < total; i++ {
		m, err := d.CreateMessage(ctx, owner, room, fmt.Sprintf("order-%d", i), fmt.Sprintf("<p>order %d</p>", i), fmt.Sprintf("order-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = m.ID
		if i > 0 && ids[i] <= ids[i-1] {
			t.Fatalf("ids not ascending at %d: %d <= %d", i, ids[i], ids[i-1])
		}
	}
}

// TestWriteQueueFailureIsolation puts one failing job in a batch of good ones
// and asserts the failure rolls back exactly its own statements: the good
// messages persist with their search rows and the unread bump, the failing
// one leaves nothing behind.
func TestWriteQueueFailureIsolation(t *testing.T) {
	d, owner, observer, stranger, room := seededWriterTest(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]struct {
		m   Message
		err error
	}, 6)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			user := owner
			if i == 2 {
				user = stranger // no membership in the room: ErrForbidden
			}
			results[i].m, results[i].err = d.CreateMessage(ctx, user, room, fmt.Sprintf("late-%d", i), fmt.Sprintf("<p>late %d</p>", i), fmt.Sprintf("late-%d", i))
		}(i)
	}
	close(start)
	wg.Wait()
	for i, r := range results {
		if i == 2 {
			if !errors.Is(r.err, ErrForbidden) {
				t.Fatalf("job %d: got %v, want ErrForbidden", i, r.err)
			}
			continue
		}
		if r.err != nil {
			t.Fatalf("job %d: %v", i, r.err)
		}
	}
	// The failing job left no message row and no search row; the others all
	// persisted with search rows, and the unread bump still ran.
	if n, err := d.readInt(ctx, "SELECT count(*) FROM messages WHERE room_id=?", room); err != nil || n != 5 {
		t.Fatalf("messages after batch = %d %v, want 5", n, err)
	}
	if n, err := d.readInt(ctx, "SELECT count(*) FROM message_search_index"); err != nil || n != 5 {
		t.Fatalf("search rows after batch = %d %v, want 5", n, err)
	}
	if n, err := d.readInt(ctx, "SELECT count(*) FROM messages WHERE client_message_id='late-2'"); err != nil || n != 0 {
		t.Fatalf("failed job persisted: %d %v", n, err)
	}
	if _, err := d.readStamp(ctx, "SELECT unread_at FROM memberships WHERE room_id=? AND user_id=?", room, observer); err != nil {
		t.Fatalf("observer unread not bumped by the batch: %v", err)
	}
}

// TestWriteQueueCancellation pins the cancel semantics: a job whose context is
// cancelled before the writer executes it does not persist (the legacy
// BeginTx-with-cancelled-ctx behaviour), and the caller learns ctx.Err().
func TestWriteQueueCancellation(t *testing.T) {
	d, owner, _, _, room := seededWriterTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m, err := d.CreateMessage(ctx, owner, room, "cancelled", "<p>cancelled</p>", "cancelled")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create: %v %+v", err, m)
	}
	if n, err := d.readInt(context.Background(), "SELECT count(*) FROM messages WHERE client_message_id='cancelled'"); err != nil || n != 0 {
		t.Fatalf("cancelled job persisted: %d %v", n, err)
	}
	// The writer still serves later jobs.
	after, err := d.CreateMessage(context.Background(), owner, room, "after-cancel", "<p>after cancel</p>", "after-cancel")
	if err != nil || after.Body != "<p>after cancel</p>" {
		t.Fatalf("post-cancel write: %v %+v", err, after)
	}
}

// TestWriteQueueDrainsOnClose pins shutdown: jobs queued before Close run to
// completion (their results are delivered), and creators racing Close either
// persist or fail with ErrWriterClosed — never hang.
func TestWriteQueueDrainsOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	d, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	room, err := d.CreateRoom(ctx, owner.ID, "Rooms::Open", "Lane", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Queued jobs whose callers never wait are still drained and answered.
	raw := make([]*messageJob, 8)
	for i := range raw {
		raw[i] = &messageJob{
			ctx:  ctx,
			run:  func(tx *sql.Tx) (Message, error) { return Message{ID: int64(i + 1)}, nil },
			done: make(chan messageResult, 1),
		}
		if err := d.writer.submit(raw[i]); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	for i, job := range raw {
		result := <-job.done
		if result.err != nil {
			t.Fatalf("queued job %d: %v", i, result.err)
		}
		if result.message.ID != int64(i+1) {
			t.Fatalf("queued job %d result = %+v", i, result.message)
		}
	}

	// Creators racing Close: every outcome is decided before Close returns.
	d, err = Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = d.CreateMessage(ctx, owner.ID, room.ID, fmt.Sprintf("close-%d", i), fmt.Sprintf("<p>close %d</p>", i), fmt.Sprintf("close-%d", i))
		}(i)
	}
	time.Sleep(2 * time.Millisecond)
	d.Close()
	wg.Wait()
	committed, failed := 0, 0
	for _, err := range errs {
		switch {
		case errors.Is(err, ErrWriterClosed):
			failed++
		case err != nil:
			t.Fatalf("close-period write: %v", err)
		default:
			committed++
		}
	}
	if committed+failed != 16 {
		t.Fatalf("outcomes = %d committed + %d failed, want 16", committed, failed)
	}
	if committed == 0 {
		t.Fatal("no write completed before close")
	}
	// Persistence is final: a fresh open of the same file sees every
	// committed row exactly once, and none of the rejected ones.
	check, err := Open(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	for i := 0; i < 16; i++ {
		n, err := check.readInt(ctx, "SELECT count(*) FROM messages WHERE client_message_id=?", fmt.Sprintf("close-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if n > 1 {
			t.Fatalf("close-%d persisted %d times", i, n)
		}
	}
	if n, err := check.readInt(ctx, "SELECT count(*) FROM messages WHERE room_id=?", room.ID); err != nil || n != committed {
		t.Fatalf("persisted = %d %v, want %d committed", n, err, committed)
	}
}

// TestCreateMessageUnreadWindow pins the connected_at cutoff of the unread
// bump: a member connected within the last minute is not marked unread, a
// member connected before it is (the same SQL on both lanes).
func TestCreateMessageUnreadWindow(t *testing.T) {
	for _, mode := range []string{"on", "off"} {
		t.Run("queue="+mode, func(t *testing.T) {
			t.Setenv("CAMPFIRE_WRITE_QUEUE", mode)
			d, owner, observer, _, room := seededWriterTest(t)
			ctx := context.Background()
			if _, err := d.Write.ExecContext(ctx, "UPDATE memberships SET connected_at=? WHERE room_id=? AND user_id=?", Stamp(time.Now().Add(-2*time.Minute)), room, observer); err != nil {
				t.Fatal(err)
			}
			if _, err := d.CreateMessage(ctx, owner, room, "", "<p>old</p>", "old"); err != nil {
				t.Fatal(err)
			}
			if _, err := d.readStamp(ctx, "SELECT unread_at FROM memberships WHERE room_id=? AND user_id=?", room, observer); err != nil {
				t.Fatalf("stale connection was not bumped: %v", err)
			}
			if _, err := d.Write.ExecContext(ctx, "UPDATE memberships SET connected_at=?, unread_at=NULL WHERE room_id=? AND user_id=?", Stamp(time.Now().Add(-10*time.Second)), room, observer); err != nil {
				t.Fatal(err)
			}
			if _, err := d.CreateMessage(ctx, owner, room, "", "<p>new</p>", "new"); err != nil {
				t.Fatal(err)
			}
			var unread sql.NullString
			if err := d.Read.QueryRowContext(ctx, "SELECT unread_at FROM memberships WHERE room_id=? AND user_id=?", room, observer).Scan(&unread); err != nil {
				t.Fatal(err)
			}
			if unread.Valid {
				t.Fatalf("recent connection marked unread: %v", unread.String)
			}
		})
	}
}

// TestWriteQueueWebhookReply exercises the background path: a webhook-style
// create (no membership check) through the queue still persists and
// search-indexes.
func TestWriteQueueWebhookReply(t *testing.T) {
	d, owner, _, stranger, room := seededWriterTest(t)
	ctx := context.Background()
	m, err := d.CreateWebhookReply(ctx, stranger, room, "<p>reply</p>", "reply", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := d.readInt(ctx, "SELECT count(*) FROM messages WHERE id=?", m.ID); err != nil || n != 1 {
		t.Fatalf("webhook reply missing: %d %v", n, err)
	}
	if hits, err := d.Search(ctx, owner, "reply"); err != nil || len(hits) != 1 {
		t.Fatalf("webhook reply not searchable: %v %v", hits, err)
	}
}

// TestWriteQueueAfterCommitFailure pins the queued path's after-commit
// contract: the message row commits in the shared transaction, and a failure
// of the search-index insert after commit is reported to the caller while the
// message stays — the same window the Rust port's after_commit hooks have
// (the direct path rolls the row back instead; see TestSchemaAndMessageTransaction).
func TestWriteQueueAfterCommitFailure(t *testing.T) {
	d, owner, _, _, room := seededWriterTest(t)
	ctx := context.Background()
	if _, err := d.Write.Exec("DROP TABLE message_search_index"); err != nil {
		t.Fatal(err)
	}
	m, err := d.CreateMessage(ctx, owner, room, "keeps-row", "<p>keeps row</p>", "keeps row")
	if err == nil {
		t.Fatal("expected the failed index insert to surface")
	}
	if n, err := d.readInt(ctx, "SELECT count(*) FROM messages WHERE client_message_id='keeps-row'"); err != nil || n != 1 {
		t.Fatalf("message row after after-commit failure = %d %v, want 1 (committed before the index)", n, err)
	}
	_ = m
}

// benchLane seeds one user and room and runs b.N message creates on the given
// lane (queue on, or the direct per-request transaction path).
func benchLane(b *testing.B, queue string) {
	b.Setenv("CAMPFIRE_WRITE_QUEUE", queue)
	d, err := Open(b.TempDir()+"/bench.sqlite3", 4)
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	user, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		b.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		b.Fatalf("rooms: %v %v", rooms, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprintf("bench-%d", i), fmt.Sprintf("<p>bench body %d</p>", i), fmt.Sprintf("bench body %d", i)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCreateMessageQueued(b *testing.B) { benchLane(b, "on") }
func BenchmarkCreateMessageDirect(b *testing.B) { benchLane(b, "off") }

// benchLaneParallel is the concurrent picture: b.RunParallel mirrors the
// harness's many simultaneous posters, where group commit shares one COMMIT
// across the drain.
func benchLaneParallel(b *testing.B, queue string) {
	b.Setenv("CAMPFIRE_WRITE_QUEUE", queue)
	d, err := Open(b.TempDir()+"/bench.sqlite3", 4)
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	user, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		b.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		b.Fatalf("rooms: %v %v", rooms, err)
	}
	sentinel := 0
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := sentinel
			sentinel++
			if _, err := d.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprintf("bench-%d", n), fmt.Sprintf("<p>bench body %d</p>", n), fmt.Sprintf("bench body %d", n)); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkCreateMessageQueuedParallel(b *testing.B) { benchLaneParallel(b, "on") }
func BenchmarkCreateMessageDirectParallel(b *testing.B) { benchLaneParallel(b, "off") }
