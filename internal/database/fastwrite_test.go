package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/fastdb/csqlite"
)

// The ENGINE-55 differential tests: identical create workloads through the
// database/sql write lane (CAMPFIRE_FASTDB_WRITE=off) and the direct fastdb
// lane (on, the production default), on twin identically seeded databases,
// compared row for row. Frozen time makes every stamp deterministic, so
// byte-for-byte equality is meaningful.

// twinDB opens two identically seeded databases with the given lane modes.
// The workload below must use explicit client ids (never ""), because the
// empty client id draws a random UUID. The stranger (user 3) has no
// membership in the closed room the workload posts to (CreateUser joins the
// open rooms only), so forbidden posts fail the membership check.
func twinDB(t *testing.T) (legacy, fast *DB, room int64) {
	t.Helper()
	t.Setenv("CAMPFIRE_FASTDB_WRITE", "off")
	legacy = seedWriter(t, "legacy")
	t.Setenv("CAMPFIRE_FASTDB_WRITE", "on")
	fast = seedWriter(t, "fast")
	var closed int64
	if err := legacy.Read.QueryRow("SELECT id FROM rooms WHERE type='Rooms::Closed'").Scan(&closed); err != nil {
		t.Fatal(err)
	}
	return legacy, fast, closed
}

func seedWriter(t *testing.T, tag string) *DB {
	t.Helper()
	t.Setenv("CAMPFIRE_WRITE_QUEUE", "on")
	t.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000")
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	path := filepath.Join(t.TempDir(), tag+".sqlite3")
	d, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ctx := context.Background()
	if _, err := d.Setup(ctx, "Owner", "owner@test", "digest"); err != nil {
		t.Fatal(err)
	}
	observer, err := d.CreateUser(ctx, "Observer", "observer@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser(ctx, "Stranger", "stranger@test", "digest", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateRoom(ctx, 1, "Rooms::Closed", "Lane", []int64{1, observer.ID}); err != nil {
		t.Fatal(err)
	}
	// One blob for the attachment posts.
	if _, err := d.Write.Exec("INSERT INTO active_storage_blobs(key,filename,content_type,metadata,service_name,byte_size,checksum,created_at) VALUES ('twin-key','note.txt','text/plain','{}','local',11,'abc',?)", Stamp(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))); err != nil {
		t.Fatal(err)
	}
	return d
}

// dumpTable renders every row of a table in id order as one comparable
// string; a difference between the twins is a lane difference. NULLs render
// empty on both lanes, so byte-equality still holds.
func dumpTable(t *testing.T, d *DB, query string) string {
	t.Helper()
	rows, err := d.Read.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	values := make([]sql.RawBytes, len(cols))
	scan := make([]any, len(cols))
	for i := range values {
		scan[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(scan...); err != nil {
			t.Fatal(err)
		}
		for i, v := range values {
			if i > 0 {
				out.WriteByte('|')
			}
			out.Write(v)
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func compareTables(t *testing.T, legacy, fast *DB, table, query string) {
	t.Helper()
	a, b := dumpTable(t, legacy, query), dumpTable(t, fast, query)
	if a != b {
		t.Fatalf("%s rows differ:\n--- legacy (%d bytes)\n%s\n--- fast (%d bytes)\n%s", table, len(a), a, len(b), b)
	}
}

// TestFastWriteCreateDifferential drives an identical create workload
// through both lanes and compares every row the create transaction touches,
// plus the returned records and the error shapes of the failing cases.
func TestFastWriteCreateDifferential(t *testing.T) {
	legacy, fast, room := twinDB(t)
	ctx := context.Background()
	var blob int64
	if err := legacy.Read.QueryRow("SELECT id FROM active_storage_blobs WHERE key='twin-key'").Scan(&blob); err != nil {
		t.Fatal(err)
	}

	type post struct {
		fn func(*DB, string) (Message, error)
	}
	workload := []post{
		{func(d *DB, id string) (Message, error) {
			return d.CreateMessage(ctx, 1, room, id, "<p>plain</p>", "plain")
		}},
		{func(d *DB, id string) (Message, error) {
			return d.CreateMessageWithBlob(ctx, 1, room, id, "<p>attached</p>", "attached", blob)
		}},
		{func(d *DB, id string) (Message, error) {
			return d.CreateWebhookReply(ctx, 3, room, "<p>reply</p>", "reply", 0)
		}},
		{func(d *DB, id string) (Message, error) {
			return d.CreateMessageWithUpload(ctx, 1, room, id, nil, "upload", &fakeStager{}, false)
		}},
		{func(d *DB, id string) (Message, error) {
			// The queued path: a blocker holds the lane (its transaction is
			// open on the single connection), so this post can only batch
			// behind it. The post runs on its own goroutine: it must be
			// submitted while the lane is still held, then the gate opens
			// and the batch commits — the shared commit is what the post
			// waits for.
			release := d.LaneBlocker()
			defer release()
			for {
				d.writer.mu.Lock()
				busy := d.writer.busy
				d.writer.mu.Unlock()
				if busy {
					break
				}
				runtime.Gosched()
			}
			type result struct {
				m   Message
				err error
			}
			posted := make(chan result, 1)
			go func() {
				m, err := d.CreateMessage(ctx, 1, room, id, "<p>queued</p>", "queued")
				posted <- result{m, err}
			}()
			time.Sleep(50 * time.Millisecond) // let the request reach the queue
			release()
			r := <-posted
			return r.m, r.err
		}},
		{func(d *DB, id string) (Message, error) {
			// Forbidden: the stranger has no membership in the room.
			return d.CreateMessage(ctx, 3, room, id, "<p>nope</p>", "nope")
		}},
		{func(d *DB, id string) (Message, error) {
			// No such creator: the webhook path skips the membership check
			// and fails the creator lookup (sql.ErrNoRows).
			return d.CreateWebhookReply(ctx, 999999, room, "<p>ghost</p>", "ghost", 0)
		}},
		{func(d *DB, id string) (Message, error) {
			// Foreign-key failure: the attachment names a blob that does not
			// exist, rolling back the whole transaction.
			return d.CreateMessageWithBlob(ctx, 1, room, id, "<p>fk</p>", "fk", 987654)
		}},
	}
	ids := make([]string, len(workload))
	for i := range workload {
		ids[i] = fmt.Sprintf("twin-%02d", i)
	}

	run := func(d *DB) ([]Message, []error) {
		messages := make([]Message, len(workload))
		errs := make([]error, len(workload))
		for i, p := range workload {
			messages[i], errs[i] = p.fn(d, ids[i])
		}
		return messages, errs
	}
	legacyMessages, legacyErrs := run(legacy)
	fastMessages, fastErrs := run(fast)

	// The webhook post's client id is generated (crypto/rand UUID when the
	// caller passes none), so the returned records are compared after
	// normalizing both sides' webhook client id to a placeholder.
	if legacyErrs[2] == nil && fastErrs[2] == nil {
		legacyMessages[2].ClientID, fastMessages[2].ClientID = "", ""
	}

	// Responses: the returned records must match field for field, including
	// ids, creator names and stamps.
	for i := range workload {
		if (legacyErrs[i] == nil) != (fastErrs[i] == nil) {
			t.Fatalf("post %d success mismatch: legacy %v, fast %v", i, legacyErrs[i], fastErrs[i])
		}
		if legacyErrs[i] == nil {
			if got := fastMessages[i]; !reflect.DeepEqual(got, legacyMessages[i]) {
				t.Fatalf("post %d response differs:\nlegacy %+v\nfast   %+v", i, legacyMessages[i], got)
			}
			continue
		}
		// Both failed: the error KINDS must match (forbidden, no-rows,
		// constraint).
		if errors.Is(legacyErrs[i], ErrForbidden) != errors.Is(fastErrs[i], ErrForbidden) {
			t.Fatalf("post %d forbidden mismatch: %v vs %v", i, legacyErrs[i], fastErrs[i])
		}
		if errors.Is(legacyErrs[i], sql.ErrNoRows) != errors.Is(fastErrs[i], sql.ErrNoRows) {
			t.Fatalf("post %d no-rows mismatch: %v vs %v", i, legacyErrs[i], fastErrs[i])
		}
		if i == 7 && !errors.Is(fastErrs[i], csqlite.ErrConstraint) {
			t.Fatalf("post %d: fast FK failure = %v, want a constraint error", i, fastErrs[i])
		}
	}

	// Every row the create transaction touches, byte for byte. The messages
	// query normalizes the webhook row's generated client id (creator_id=3
	// is the stranger, whose only posts are webhooks) so the comparison
	// stays deterministic; every other row carries an explicit client id.
	compareTables(t, legacy, fast, "messages", "SELECT id,CASE WHEN creator_id=3 THEN '{webhook}' ELSE client_message_id END,creator_id,room_id,created_at,updated_at FROM messages ORDER BY id")
	compareTables(t, legacy, fast, "action_text_rich_texts", "SELECT id,record_type,record_id,name,body,created_at,updated_at FROM action_text_rich_texts ORDER BY id")
	compareTables(t, legacy, fast, "active_storage_attachments", "SELECT id,blob_id,record_type,record_id,name,created_at FROM active_storage_attachments ORDER BY id")
	compareTables(t, legacy, fast, "active_storage_blobs", "SELECT id,key,filename,content_type,metadata,service_name,byte_size,checksum,created_at FROM active_storage_blobs ORDER BY id")
	compareTables(t, legacy, fast, "message_search_index", "SELECT rowid,body FROM message_search_index ORDER BY rowid")
	compareTables(t, legacy, fast, "memberships", "SELECT id,room_id,user_id,involvement,unread_at,updated_at,connected_at,connections,created_at FROM memberships ORDER BY id")
	compareTables(t, legacy, fast, "rooms", "SELECT id,creator_id,name,type,created_at,updated_at FROM rooms ORDER BY id")

	// The error cases left the same rolled-back state on both lanes: the
	// forbidden, ghost-creator and FK posts persisted nothing, the five
	// successful posts persisted with their search rows.
	for _, id := range []string{"twin-05", "twin-06", "twin-07"} {
		for name, d := range map[string]*DB{"legacy": legacy, "fast": fast} {
			if n, err := countRows(d, "SELECT count(*) FROM messages WHERE client_message_id=?", id); err != nil || n != 0 {
				t.Fatalf("%s: failed post %s persisted: %d %v", name, id, n, err)
			}
		}
	}
	for name, d := range map[string]*DB{"legacy": legacy, "fast": fast} {
		if n, err := countRows(d, "SELECT count(*) FROM messages WHERE room_id=?", room); err != nil || n != 5 {
			t.Fatalf("%s: messages = %d %v, want 5", name, n, err)
		}
		if n, err := countRows(d, "SELECT count(*) FROM message_search_index"); err != nil || n != 5 {
			t.Fatalf("%s: search rows = %d %v, want 5", name, n, err)
		}
	}
}

// fakeStager inserts a fixed blob row through the lane's UploadTx surface,
// exercising the direct ExecContext conversion, including a NULL
// content_type like a real upload without one.
type fakeStager struct{}

func (s *fakeStager) Insert(ctx context.Context, tx UploadTx) (int64, error) {
	result, err := tx.ExecContext(ctx,
		"INSERT INTO active_storage_blobs(key,filename,content_type,metadata,service_name,byte_size,checksum,created_at) VALUES (?,?,?,?,?,?,?,?)",
		"staged-key", "staged.txt", (*string)(nil), "{}", "local", 7, "checksum", Stamp(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *fakeStager) Keep()    {}
func (s *fakeStager) Discard() {}

// TestFastWriteUploadConversion drives a staged upload through both lanes
// and compares the blob rows: the direct lane's ExecContext conversion must
// store exactly what the database/sql lane stores.
func TestFastWriteUploadConversion(t *testing.T) {
	legacy, fast, room := twinDB(t)
	ctx := context.Background()
	// The same client id on both twins: each twin is its own database, so
	// the rows must be byte-identical.
	if _, err := legacy.CreateMessageWithUpload(ctx, 1, room, "upload-twin", nil, "upload", &fakeStager{}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := fast.CreateMessageWithUpload(ctx, 1, room, "upload-twin", nil, "upload", &fakeStager{}, false); err != nil {
		t.Fatal(err)
	}
	compareTables(t, legacy, fast, "active_storage_blobs", "SELECT id,key,filename,content_type,metadata,service_name,byte_size,checksum,created_at FROM active_storage_blobs ORDER BY id")
	compareTables(t, legacy, fast, "messages", "SELECT id,client_message_id,creator_id,room_id,created_at,updated_at FROM messages ORDER BY id")
	compareTables(t, legacy, fast, "active_storage_attachments", "SELECT id,blob_id,record_type,record_id,name,created_at FROM active_storage_attachments ORDER BY id")
}

// TestFastWriteConcurrentStress drives many concurrent creators through the
// direct lane (group commit plus after-commit transactions interleaved on
// the single connection) and asserts every request's result, every row and
// every search row, plus the unread bump — the -race gate watches the
// connection serialization.
func TestFastWriteConcurrentStress(t *testing.T) {
	t.Setenv("CAMPFIRE_FASTDB_WRITE", "on")
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	observer, err := d.CreateUser(ctx, "Observer", "observer@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	room, err := d.CreateRoom(ctx, owner.ID, "Rooms::Open", "Stress", []int64{owner.ID, observer.ID})
	if err != nil {
		t.Fatal(err)
	}
	const writers = 24
	const perWriter = 5
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([][]Message, writers)
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < perWriter; j++ {
				body := fmt.Sprintf("<p>stress %d %d</p>", i, j)
				plain := fmt.Sprintf("sneedle%02d%02d", i, j)
				m, err := d.CreateMessage(ctx, owner.ID, room.ID, fmt.Sprintf("stress-%d-%d", i, j), body, plain)
				if err != nil {
					errs[i] = err
					return
				}
				results[i] = append(results[i], m)
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
	seen := make(map[int64]bool, writers*perWriter)
	for _, list := range results {
		for _, m := range list {
			if m.RoomID != room.ID || m.CreatorID != owner.ID || m.Creator != "Owner" {
				t.Fatalf("bad record %+v", m)
			}
			if seen[m.ID] {
				t.Fatalf("duplicate id %d", m.ID)
			}
			seen[m.ID] = true
		}
	}
	if len(seen) != writers*perWriter {
		t.Fatalf("distinct ids = %d, want %d", len(seen), writers*perWriter)
	}
	if n, err := countRows(d, "SELECT count(*) FROM messages WHERE room_id=?", room.ID); err != nil || n != writers*perWriter {
		t.Fatalf("messages = %d %v, want %d", n, err, writers*perWriter)
	}
	for i := 0; i < writers; i++ {
		for j := 0; j < perWriter; j++ {
			hits, err := d.Search(ctx, owner.ID, fmt.Sprintf("sneedle%02d%02d", i, j))
			if err != nil || len(hits) != 1 {
				t.Fatalf("search %d-%d: %v %v", i, j, hits, err)
			}
		}
	}
	var unread sql.NullString
	if err := d.Read.QueryRowContext(ctx, "SELECT unread_at FROM memberships WHERE room_id=? AND user_id=?", room.ID, observer.ID).Scan(&unread); err != nil || !unread.Valid {
		t.Fatalf("observer unread not bumped: %v %v", unread, err)
	}
}

func countRows(d *DB, query string, args ...any) (int, error) {
	var n int
	err := d.Read.QueryRow(query, args...).Scan(&n)
	return n, err
}

// TestFastWriteStatementStream pins the direct lane's per-post statement
// stream by text and order: the create transaction's statements, transaction
// control included, on both the in-line and the queued path. The legacy
// lane's stream is pinned by the same texts through the counting harness
// (internal/web TestCreateMessageStatementCount), and the texts themselves
// are the shared lane constants, so the two lanes cannot drift.
func TestFastWriteStatementStream(t *testing.T) {
	old := fastWriteRecord
	fastWriteRecord = nil
	defer func() { fastWriteRecord = old }()
	var recMu sync.Mutex
	var recorded []string
	fastWriteRecord = func(q string) {
		recMu.Lock()
		recorded = append(recorded, q)
		recMu.Unlock()
	}
	reset := func() {
		recMu.Lock()
		recorded = nil
		recMu.Unlock()
	}
	snapshot := func() []string {
		recMu.Lock()
		defer recMu.Unlock()
		return append([]string(nil), recorded...)
	}

	// The recorder is captured at open: open the database after installing
	// it.
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	observer, err := d.CreateUser(ctx, "Observer", "observer@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	room, err := d.CreateRoom(ctx, owner.ID, "Rooms::Open", "Stream", []int64{owner.ID, observer.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO active_storage_blobs(key,filename,content_type,metadata,service_name,byte_size,checksum,created_at) VALUES ('stream-key','a.txt','text/plain','{}','local',1,'x',?)", Stamp(time.Now())); err != nil {
		t.Fatal(err)
	}
	var blob int64
	if err := d.Read.QueryRow("SELECT id FROM active_storage_blobs WHERE key='stream-key'").Scan(&blob); err != nil {
		t.Fatal(err)
	}
	recorded = nil // drop the seeding statements; keep only the posts

	// In-line plain post: the eight create statements plus the transaction
	// control, in the create transaction's order.
	reset()
	if _, err := d.CreateMessage(ctx, owner.ID, room.ID, "stream-1", "<p>one</p>", "one"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"BEGIN IMMEDIATE",
		stmtMembershipCount,
		stmtCreatorName,
		stmtInsertMessage,
		stmtTouchRoom,
		stmtInsertRichText,
		stmtInsertSearchIndex,
		stmtBumpUnread,
		"COMMIT",
	}
	if got := snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("in-line stream = %v\nwant %v", got, want)
	}

	// Attachment post adds the attachment statement between the rich text
	// and the after-commit pair.
	reset()
	if _, err := d.CreateMessageWithBlob(ctx, owner.ID, room.ID, "stream-2", "<p>two</p>", "two", blob); err != nil {
		t.Fatal(err)
	}
	want = []string{
		"BEGIN IMMEDIATE",
		stmtMembershipCount,
		stmtCreatorName,
		stmtInsertMessage,
		stmtTouchRoom,
		stmtInsertRichText,
		stmtInsertAttachment,
		stmtInsertSearchIndex,
		stmtBumpUnread,
		"COMMIT",
	}
	if got := snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("attachment stream = %v\nwant %v", got, want)
	}

	// The queued path: one post queues behind a blocker, so the caller's
	// after-commit pair runs in a transaction of its own after the shared
	// commit. A single-job batch runs directly in the transaction — no
	// savepoint — which is the lane's documented shape; the SAVEPOINT w /
	// ROLLBACK TO w / RELEASE w statements of a multi-job batch are pinned
	// at the connection level (internal/fastdb/write
	// TestSavepointStatements) and behaviourally on both lanes
	// (TestWriteQueueFailureIsolation). The post runs on its own goroutine
	// (it must queue behind the blocker, whose gate the test goroutine
	// opens); the blocker's own transaction leads the recorded stream — it
	// is part of the pinned lane behaviour, like every statement before it.
	// Reset before the blocker submits: its BEGIN IMMEDIATE is then the
	// first record of the pinned stream (the busy mark is set before the
	// begin, so the wait below plus this poll see it deterministically).
	reset()
	release := d.LaneBlocker()
	defer release()
	for {
		d.writer.mu.Lock()
		busy := d.writer.busy
		d.writer.mu.Unlock()
		if busy {
			break
		}
		runtime.Gosched()
	}
	for {
		recMu.Lock()
		n := len(recorded)
		recMu.Unlock()
		if n > 0 {
			break
		}
		runtime.Gosched()
	}
	type result struct {
		m   Message
		err error
	}
	posted := make(chan result, 1)
	go func() {
		m, err := d.CreateMessage(ctx, owner.ID, room.ID, "stream-3", "<p>queued</p>", "queued")
		posted <- result{m, err}
	}()
	time.Sleep(50 * time.Millisecond) // let the request reach the queue
	release()
	if r := <-posted; r.err != nil {
		t.Fatal(r.err)
	}
	job := []string{
		stmtMembershipCount,
		stmtCreatorName,
		stmtInsertMessage,
		stmtTouchRoom,
		stmtInsertRichText,
	}
	after := []string{
		"BEGIN IMMEDIATE",
		stmtInsertSearchIndex,
		stmtBumpUnread,
		"COMMIT",
	}
	want = []string{"BEGIN IMMEDIATE", "COMMIT"} // the blocker's transaction
	want = append(want, "BEGIN IMMEDIATE")
	want = append(want, job...)
	want = append(want, "COMMIT")
	want = append(want, after...)
	if got := snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("queued stream = %v\nwant %v", got, want)
	}
}
