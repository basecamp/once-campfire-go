package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/fastdb/write"
)

// The write lane's statement surface (ENGINE-55): the message create
// transaction runs against a laneTx, implemented either over *sql.Tx
// (CAMPFIRE_FASTDB_WRITE=off, database/sql exactly as before) or over the
// direct fastdb transaction (the default). The SQL texts live once, here;
// both lanes execute these exact texts, and the statement-count tests pin
// the per-post order and count on both.
const (
	// The create transaction, in execution order: the authorized creator
	// lookup (the membership check and the name lookup in one statement), the
	// message row, the room touch, the rich-text body and the attachment
	// (when present), then the after-commit pair — search index row first,
	// unread bump second — inside the job's transaction on the direct and
	// in-line paths, in a writer-owned transaction of its own on the queued
	// path (ENGINE-45 batching).
	stmtAuthorizedCreatorName = "SELECT u.name FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.room_id=? AND m.user_id=? AND u.status=0"
	// stmtCreatorName is the webhook path's lookup: it skips the membership
	// check, so a missing or inactive user is no-rows rather than forbidden.
	stmtCreatorName       = "SELECT name FROM users WHERE id=?"
	stmtInsertMessage     = "INSERT INTO messages(client_message_id,creator_id,room_id,created_at,updated_at) VALUES (?,?,?,?,?)"
	stmtTouchRoom         = "UPDATE rooms SET updated_at=? WHERE id=?"
	stmtInsertRichText    = "INSERT INTO action_text_rich_texts(name,record_type,record_id,body,created_at,updated_at) VALUES ('body','Message',?,?,?,?)"
	stmtInsertAttachment  = "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'Message',?,'attachment',?)"
	stmtInsertSearchIndex = "INSERT INTO message_search_index(rowid,body) VALUES (?,?)"
	stmtBumpUnread        = "UPDATE memberships SET unread_at=?,updated_at=? WHERE room_id=? AND user_id!=? AND involvement!='invisible' AND (connected_at IS NULL OR connected_at < ?)"
)

// UploadTx is the statement surface a staged upload inserts its blob row
// through: *sql.Tx on the database/sql paths (Setup, CreateUser, record
// updates) and the direct lane's transaction on the fastdb path. Only the
// blob insert's one INSERT runs on it.
type UploadTx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// jobTx is the statement surface a message create job runs against: the
// create transaction's statements in SQL bind order (the parameters are
// named for the domain and bound in the placeholder order), plus the
// staged-blob surface for message uploads.
type jobTx interface {
	UploadTx
	// AuthorizedCreatorName binds (?=room_id, ?=user_id) and returns the
	// creator's name only when the user is active and a member of the room.
	// No matching row is ErrForbidden: the membership check and the name
	// lookup are one statement, so a missing, inactive or non-member user is
	// indistinguishable from the two-read version's ErrForbidden.
	AuthorizedCreatorName(ctx context.Context, room, user int64) (string, error)
	// CreatorName binds (?=id) and returns the name without a membership
	// check (the webhook path). A missing or inactive user is sql.ErrNoRows.
	CreatorName(ctx context.Context, user int64) (string, error)
	// InsertMessage binds (?=client_message_id, ?=creator_id, ?=room_id,
	// ?=created_at, ?=updated_at) and returns the new row id.
	InsertMessage(ctx context.Context, client string, user, room int64, stamp string) (int64, error)
	// TouchRoom binds (?=updated_at, ?=id).
	TouchRoom(ctx context.Context, room int64, stamp string) error
	// InsertRichText binds (?=record_id, ?=body, ?=created_at, ?=updated_at).
	InsertRichText(ctx context.Context, id int64, body, stamp string) error
	// InsertAttachment binds (?=blob_id, ?=record_id, ?=created_at).
	InsertAttachment(ctx context.Context, blob, id int64, stamp string) error
	// InsertSearchIndex binds (?=rowid, ?=body).
	InsertSearchIndex(ctx context.Context, id int64, plain string) error
	// BumpUnread binds (?=unread_at, ?=updated_at, ?=room_id, ?=user_id,
	// ?=connected_at cutoff).
	BumpUnread(ctx context.Context, room, user int64, stamp, cutoff string) error
}

// laneTx is a job's transaction on the write lane: transaction control plus
// the create statements. Implementations are sqlLaneTx over *sql.Tx and
// fastLaneTx over the direct fastdb transaction.
type laneTx interface {
	jobTx
	commit() error
	rollback() error
	// savepoint, rollbackTo and releaseSavepoint run the SAVEPOINT w /
	// ROLLBACK TO w / RELEASE w statements the batch path wraps each job
	// in, so a failing job rolls back exactly its own statements.
	savepoint() error
	rollbackTo() error
	releaseSavepoint() error
}

// laneConn begins transactions on the write lane. The database/sql lane
// wraps the write pool (one connection); the fastdb lane wraps one direct
// connection, serialized at transaction granularity.
type laneConn interface {
	begin(ctx context.Context) (laneTx, error)
	// close releases the lane's own connection, if any, after the writer
	// drained and the after-commit count reached zero. The database/sql
	// lane's pool is closed through DB.Close like every other pool.
	close() error
	// walFrames reports the WAL's frame count as of the lane connection's
	// most recent commit (0 when unavailable). It is not free: the fastdb lane
	// takes the connection's Go mutex and makes one cgo call (off the commit
	// path, once per drain); the database/sql lane probes PRAGMA
	// wal_checkpoint(NOOP), which does no work, at most once per
	// checkpointProbe. It is the writer's WAL-growth signal.
	walFrames() int64
	// backfillRestart runs PASSIVE then RESTART on the lane connection,
	// between transactions: PASSIVE backfills every frame a reader is not
	// pinning, and RESTART waits for the readers so the next write resets
	// the WAL. The outcome reports whether SQLite actually completed the
	// restart — it reports a refused checkpoint in the pragma's result row,
	// not as an execution error — and the attempt runs with a bounded busy
	// timeout (write.CheckpointBusyMS) so a pinned reader cannot stall the
	// writer for the connection's full busy timeout.
	backfillRestart() (checkpointOutcome, error)
}

// checkpointOutcome is one WAL restart attempt's result. attempted is false
// only when the lane could not run the pragmas at all; completed is false when
// SQLite reported the checkpoint incomplete (a reader pinning frames, or
// another checkpoint holding the lock), which is the case the writer must
// count separately from a restart that actually reset the WAL. passiveNanos
// and restartNanos split the attempt's wall time into the backfill step
// (page copying and its filesystem sync) and the RESTART step (which includes
// the busy-handler wait).
type checkpointOutcome struct {
	attempted    bool
	completed    bool
	frames       int64
	backfilled   int64
	passiveNanos int64
	restartNanos int64
}

// sqlLaneConn is the database/sql lane: the existing write pool, whose
// SetMaxOpenConns(1) serializes the lane and the after-commit transactions
// exactly as before.
type sqlLaneConn struct {
	db *sql.DB
	// probeMu guards the WAL probe's throttle state. The database/sql lane
	// has no WAL hook, so it reads the frame count with
	// PRAGMA wal_checkpoint(NOOP) — a statement that does no work — and
	// throttles it to checkpointProbe so a burst of drains cannot turn the
	// limit check into a per-post statement.
	probeMu   sync.Mutex
	probeLast time.Time
	probeVal  int64
	// restoreStmt overrides the statement that restores the pool's normal busy
	// timeout after a checkpoint attempt (empty means stmtSQLRestoreBusy). It
	// exists so a test can make the restore fail and prove the connection is
	// discarded rather than returned with the checkpoint's timeout still set.
	restoreStmt string
}

// checkpointProbe bounds how often the database/sql lane reads the WAL frame
// count with the NOOP pragma.
const checkpointProbe = 5 * time.Millisecond

func (c *sqlLaneConn) begin(ctx context.Context) (laneTx, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlLaneTx{tx: tx}, nil
}

func (c *sqlLaneConn) close() error { return nil }

func (c *sqlLaneConn) walFrames() int64 {
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	if now := time.Now(); now.Sub(c.probeLast) < checkpointProbe {
		return c.probeVal
	}
	var busy, log, checked int
	if err := c.db.QueryRow("PRAGMA wal_checkpoint(NOOP)").Scan(&busy, &log, &checked); err != nil {
		return c.probeVal
	}
	c.probeLast = time.Now()
	c.probeVal = int64(log)
	return c.probeVal
}

// The database/sql lane's checkpoint statements are prebuilt for the same
// reason as the fastdb lane's: the pool prepares per text.
var (
	stmtSQLCheckpointBusy = "PRAGMA busy_timeout = " + strconv.Itoa(write.CheckpointBusyMS)
	stmtSQLRestoreBusy    = "PRAGMA busy_timeout = " + strconv.Itoa(write.BusyTimeoutMS)
)

// backfillRestart mirrors the fastdb lane's CheckpointRestart on the write
// pool. It reserves one pool connection for the whole sequence: the busy
// timeout is a property of the connection, and SetMaxOpenConns(1) serializes
// individual operations without reserving the connection across them, so
// without the reservation an unrelated borrower (a session or account write, a
// queued after-commit statement) could run between the steps and inherit the
// checkpoint's 250 ms timeout. The timeout is restored on the same connection
// before it goes back to the pool; if the restore fails, the connection is
// discarded rather than returned misconfigured.
func (c *sqlLaneConn) backfillRestart() (checkpointOutcome, error) {
	ctx := context.Background()
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return checkpointOutcome{}, err
	}
	out, err := c.backfillRestartOn(ctx, conn)
	restore := c.restoreStmt
	if restore == "" {
		restore = stmtSQLRestoreBusy
	}
	if _, restoreErr := conn.ExecContext(ctx, restore); restoreErr != nil {
		// Never hand a connection whose busy timeout is still the checkpoint's
		// back to the pool: Raw with driver.ErrBadConn discards it, and the
		// pool opens a fresh one (with the DSN's timeout) on the next use.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		if err == nil {
			err = restoreErr
		}
		return out, err
	}
	if closeErr := conn.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return out, err
}

func (c *sqlLaneConn) backfillRestartOn(ctx context.Context, conn *sql.Conn) (checkpointOutcome, error) {
	if _, err := conn.ExecContext(ctx, stmtSQLCheckpointBusy); err != nil {
		return checkpointOutcome{}, err
	}
	out := checkpointOutcome{attempted: true}
	passiveStart := time.Now()
	if _, err := conn.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		out.passiveNanos = time.Since(passiveStart).Nanoseconds()
		return out, err
	}
	out.passiveNanos = time.Since(passiveStart).Nanoseconds()
	restartStart := time.Now()
	var busy, log, checked int64
	err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(RESTART)").Scan(&busy, &log, &checked)
	out.restartNanos = time.Since(restartStart).Nanoseconds()
	if err != nil {
		return out, err
	}
	out.completed = busy == 0
	out.frames = log
	out.backfilled = checked
	return out, nil
}

// sqlLaneTx adapts *sql.Tx to the job and lane surfaces with the same calls
// the create path always made — same SQL texts, same bind order, same
// statements inside the job's transaction.
type sqlLaneTx struct {
	tx *sql.Tx
}

func (t *sqlLaneTx) AuthorizedCreatorName(ctx context.Context, room, user int64) (string, error) {
	var name string
	err := t.tx.QueryRowContext(ctx, stmtAuthorizedCreatorName, room, user).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrForbidden
	}
	return name, err
}

func (t *sqlLaneTx) CreatorName(ctx context.Context, user int64) (string, error) {
	var name string
	err := t.tx.QueryRowContext(ctx, stmtCreatorName, user).Scan(&name)
	return name, err
}

func (t *sqlLaneTx) InsertMessage(ctx context.Context, client string, user, room int64, stamp string) (int64, error) {
	r, err := t.tx.ExecContext(ctx, stmtInsertMessage, client, user, room, stamp, stamp)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (t *sqlLaneTx) TouchRoom(ctx context.Context, room int64, stamp string) error {
	_, err := t.tx.ExecContext(ctx, stmtTouchRoom, stamp, room)
	return err
}

func (t *sqlLaneTx) InsertRichText(ctx context.Context, id int64, body, stamp string) error {
	_, err := t.tx.ExecContext(ctx, stmtInsertRichText, id, body, stamp, stamp)
	return err
}

func (t *sqlLaneTx) InsertAttachment(ctx context.Context, blob, id int64, stamp string) error {
	_, err := t.tx.ExecContext(ctx, stmtInsertAttachment, blob, id, stamp)
	return err
}

func (t *sqlLaneTx) InsertSearchIndex(ctx context.Context, id int64, plain string) error {
	_, err := t.tx.ExecContext(ctx, stmtInsertSearchIndex, id, plain)
	return err
}

func (t *sqlLaneTx) BumpUnread(ctx context.Context, room, user int64, stamp, cutoff string) error {
	_, err := t.tx.ExecContext(ctx, stmtBumpUnread, stamp, stamp, room, user, cutoff)
	return err
}

func (t *sqlLaneTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

func (t *sqlLaneTx) commit() error { return t.tx.Commit() }
func (t *sqlLaneTx) rollback() error {
	return t.tx.Rollback()
}
func (t *sqlLaneTx) savepoint() error {
	_, err := t.tx.Exec("SAVEPOINT w")
	return err
}
func (t *sqlLaneTx) rollbackTo() error {
	_, err := t.tx.Exec("ROLLBACK TO w")
	return err
}
func (t *sqlLaneTx) releaseSavepoint() error {
	_, err := t.tx.Exec("RELEASE w")
	return err
}
