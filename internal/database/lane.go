package database

import (
	"context"
	"database/sql"
)

// The write lane's statement surface (ENGINE-55): the message create
// transaction runs against a laneTx, implemented either over *sql.Tx
// (CAMPFIRE_FASTDB_WRITE=off, database/sql exactly as before) or over the
// direct fastdb transaction (the default). The SQL texts live once, here;
// both lanes execute these exact texts, and the statement-count tests pin
// the per-post order and count on both.
const (
	// The create transaction, in execution order: the membership check, the
	// creator name, the message row, the room touch, the rich-text body and
	// the attachment (when present), then the after-commit pair — search
	// index row first, unread bump second — inside the job's transaction on
	// the direct and in-line paths, in a transaction of its own on the
	// queued path (ENGINE-45 batching).
	stmtMembershipCount   = "SELECT count(*) FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.room_id=? AND m.user_id=? AND u.status=0"
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
	// MembershipCount binds (?=room_id, ?=user_id).
	MembershipCount(ctx context.Context, room, user int64) (int64, error)
	// CreatorName binds (?=id).
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
}

// sqlLaneConn is the database/sql lane: the existing write pool, whose
// SetMaxOpenConns(1) serializes the lane and the after-commit transactions
// exactly as before.
type sqlLaneConn struct {
	db *sql.DB
}

func (c *sqlLaneConn) begin(ctx context.Context) (laneTx, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlLaneTx{tx: tx}, nil
}

func (c *sqlLaneConn) close() error { return nil }

// sqlLaneTx adapts *sql.Tx to the job and lane surfaces with the same calls
// the create path always made — same SQL texts, same bind order, same
// statements inside the job's transaction.
type sqlLaneTx struct {
	tx *sql.Tx
}

func (t *sqlLaneTx) MembershipCount(ctx context.Context, room, user int64) (int64, error) {
	var n int64
	err := t.tx.QueryRowContext(ctx, stmtMembershipCount, room, user).Scan(&n)
	return n, err
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
