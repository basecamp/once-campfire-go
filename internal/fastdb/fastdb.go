// Package fastdb is the engine's thin SQLite read layer. It talks to SQLite
// through internal/fastdb/csqlite with positional columns and caller-owned
// destination buffers, so the hot scans allocate nothing, while the legacy
// database/sql readers stay the reference for every query shape.
//
// The query methods mirror specific internal/database readers exactly — same
// SQL, same column order, same decoding (including internal/database's stamp
// layouts) — and the differential tests assert field equality against those
// readers on the same database file.
//
// # Concurrency
//
// A Conn is not safe for concurrent use: it owns one SQLite connection, one
// statement cache and scratch buffers. Open one Conn per goroutine, as the
// read pool does with database/sql connections. Close finalizes every cached
// statement before closing the connection.
//
// # Reader contract
//
// OpenReadOnly applies the pragma set the internal/database read DSN applies
// (busy_timeout=5000, foreign_keys=on, journal_mode=WAL, synchronous=NORMAL,
// cache_size=2000) plus query_only, and opens the file read-only. A database
// that is not in WAL mode therefore fails to open on a read-only handle,
// exactly as the database/sql readers do. Prepared statements are cached by
// query text with the same 256-entry cap as internal/database's read pool:
// once the cap is reached, further query texts are prepared per use.
//
// # CGO safety
//
// The Conn, statements and all C pointers live in Go memory or SQLite's own
// allocations; no Go pointer is stored in C. Bytes returned by SQLite are
// copied into caller buffers before the next step, and bound text is handed to
// SQLite with SQLITE_TRANSIENT (copied during the call). See package csqlite
// for the precise boundary.
package fastdb

import (
	"database/sql"
	"time"

	"github.com/basecamp/once-campfire-go/internal/fastdb/csqlite"
)

// ErrNoRows aliases database/sql's sentinel: single-row lookups report the
// same error the legacy readers return, so callers can move between the two
// without changing error checks.
var ErrNoRows = sql.ErrNoRows

// Room mirrors the room columns internal/database decodes.
type Room struct {
	ID, CreatorID int64
	Name, Type    string
	UpdatedAt     time.Time
}

// MessageRef is the reduced message reference internal/database's
// MessagePageReferences scans: the message id, its room (from the query
// argument) and its update stamp. RoomID is set by the writer.
type MessageRef struct {
	ID        int64
	RoomID    int64
	UpdatedAt time.Time
}

// Message mirrors the full message record internal/database decodes.
type Message struct {
	ID, RoomID, CreatorID   int64
	ClientID, Body, Creator string
	CreatedAt, UpdatedAt    time.Time
}

// User mirrors internal/database's user columns.
type User struct {
	ID                    int64
	Name, Email, Password string
	Bio, BotToken         string
	UpdatedAt             time.Time
	Role, Status          int
}

// SidebarRoom is one sidebar row: the room plus the membership state the
// sidebar join reads.
type SidebarRoom struct {
	Room
	Involvement string
	Unread      bool
}

// Conn is one read-only SQLite connection with a statement cache. Not safe
// for concurrent use; see the package comment.
type Conn struct {
	db    *csqlite.Conn
	limit int
	stmts map[string]*csqlite.Stmt
	stamp []byte
}

// OpenReadOnly opens path read-only and applies the internal/database reader
// pragmas. maxStatements caps the prepared-statement cache; values below one
// mean the default of 256, mirroring internal/database's read pool.
func OpenReadOnly(path string, maxStatements int) (*Conn, error) {
	db, err := csqlite.OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Conn, error) {
		db.Close()
		return nil, err
	}
	if maxStatements <= 0 {
		maxStatements = 256
	}
	conn := &Conn{
		db:    db,
		limit: maxStatements,
		stmts: make(map[string]*csqlite.Stmt, maxStatements),
	}
	// The pragma set the read DSN passes:
	//   ?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL
	//   &_synchronous=NORMAL&_cache_size=2000&mode=ro&_query_only=on
	for _, pragma := range [...]string{
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA foreign_keys = 1;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA query_only = 1;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA cache_size = 2000;",
	} {
		if err := db.Exec(pragma); err != nil {
			return fail(err)
		}
	}
	return conn, nil
}

// Close finalizes the cached statements and closes the connection. It is
// idempotent.
func (c *Conn) Close() error {
	if c.db == nil {
		return nil
	}
	var first error
	for query, st := range c.stmts {
		st.Reset()
		if err := st.Finalize(); err != nil && first == nil {
			first = err
		}
		delete(c.stmts, query)
	}
	if err := c.db.Close(); err != nil && first == nil {
		first = err
	}
	c.db = nil
	return first
}

// stmtRef is one acquisition from the statement cache. release drops the most
// recent step error (the caller already saw it) and resets the statement; an
// ephemeral statement is finalized because it never entered the cache.
type stmtRef struct {
	st        *csqlite.Stmt
	ephemeral bool
}

func (r *stmtRef) release() {
	r.st.Reset()
	if r.ephemeral {
		r.st.Finalize()
	}
}

// acquire returns the cached statement for query, preparing and caching it
// while the cap allows; past the cap it prepares per use.
func (c *Conn) acquire(query string) (stmtRef, error) {
	if st, ok := c.stmts[query]; ok {
		return stmtRef{st: st}, nil
	}
	st, err := c.db.Prepare(query)
	if err != nil {
		return stmtRef{}, err
	}
	if len(c.stmts) >= c.limit {
		return stmtRef{st: st, ephemeral: true}, nil
	}
	c.stmts[query] = st
	return stmtRef{st: st}, nil
}

// The SQL below is copied from internal/database; the differential tests hold
// the two implementations to the same rows.

const userColumns = "u.id,u.name,coalesce(u.email_address,''),coalesce(u.password_digest,''),u.role,u.status,coalesce(u.bio,''),u.updated_at,coalesce(u.bot_token,'')"

const (
	queryRoom = "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE m.user_id=? AND r.id=?"

	queryInvolvement = "SELECT involvement FROM memberships WHERE user_id=? AND room_id=?"

	querySessionUser = "SELECT " + userColumns + " FROM users u JOIN sessions s ON s.user_id=u.id WHERE s.token=? AND u.status=0"

	querySidebarRooms = "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at,coalesce(m.involvement,''),m.unread_at IS NOT NULL FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE m.user_id=? AND m.involvement!='invisible' ORDER BY lower(r.name)"

	messageSelect = "SELECT m.id,m.room_id,m.creator_id,m.client_message_id,coalesce(t.body,''),coalesce(u.name,''),m.created_at,m.updated_at FROM messages m LEFT JOIN users u ON u.id=m.creator_id LEFT JOIN action_text_rich_texts t ON t.record_type='Message' AND t.record_id=m.id AND t.name='body' "

	queryRefsLatest = "SELECT id,updated_at FROM messages WHERE room_id=? ORDER BY created_at DESC LIMIT 40"
	queryRefsBefore = "SELECT id,updated_at FROM messages WHERE room_id=? AND created_at < (SELECT created_at FROM messages WHERE id=? AND room_id=?) ORDER BY created_at DESC LIMIT 40"

	queryMessagesLatest = messageSelect + "WHERE m.room_id=? ORDER BY m.created_at DESC LIMIT 40"
	queryMessagesBefore = messageSelect + "WHERE m.room_id=? AND m.created_at < (SELECT created_at FROM messages WHERE id=? AND room_id=?) ORDER BY m.created_at DESC LIMIT 40"
	queryMessagesAfter  = messageSelect + "WHERE m.room_id=? AND m.created_at>? ORDER BY m.created_at LIMIT 40"
	queryMessageByID    = messageSelect + "WHERE m.room_id=? AND m.id=?"
	queryAnchorStamp    = "SELECT created_at FROM messages WHERE id=? AND room_id=?"
)

// Room mirrors database.DB.Room: the room joined to the caller's membership.
// dst is written only on success.
func (c *Conn) Room(dst *Room, user, id int64) error {
	h, err := c.acquire(queryRoom)
	if err != nil {
		return err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return err
	}
	if err := h.st.BindInt64(1, user); err != nil {
		return err
	}
	if err := h.st.BindInt64(2, id); err != nil {
		return err
	}
	rows := Rows{stmt: h.st}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return ErrNoRows
	}
	var room Room
	room.ID = rows.Int64(0)
	room.CreatorID = rows.Int64(1)
	if room.Name, err = rows.Text(2); err != nil {
		return err
	}
	if room.Type, err = rows.Text(3); err != nil {
		return err
	}
	if room.UpdatedAt, err = rows.Stamp(4); err != nil {
		return err
	}
	*dst = room
	return nil
}

// Involvement mirrors database.DB.Involvement: the caller's membership
// involvement in one room.
func (c *Conn) Involvement(user, room int64) (string, error) {
	h, err := c.acquire(queryInvolvement)
	if err != nil {
		return "", err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return "", err
	}
	if err := h.st.BindInt64(1, user); err != nil {
		return "", err
	}
	if err := h.st.BindInt64(2, room); err != nil {
		return "", err
	}
	rows := Rows{stmt: h.st}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", ErrNoRows
	}
	return rows.Text(0)
}

// SessionUser mirrors database.DB.SessionUser: the active user for a session
// token. dst is written only on success.
func (c *Conn) SessionUser(dst *User, token string) error {
	h, err := c.acquire(querySessionUser)
	if err != nil {
		return err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return err
	}
	if err := h.st.BindText(1, token); err != nil {
		return err
	}
	rows := Rows{stmt: h.st}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return ErrNoRows
	}
	var user User
	user.ID = rows.Int64(0)
	if user.Name, err = rows.Text(1); err != nil {
		return err
	}
	if user.Email, err = rows.Text(2); err != nil {
		return err
	}
	if user.Password, err = rows.Text(3); err != nil {
		return err
	}
	user.Role = int(rows.Int64(4))
	user.Status = int(rows.Int64(5))
	if user.Bio, err = rows.Text(6); err != nil {
		return err
	}
	if user.UpdatedAt, err = rows.Stamp(7); err != nil {
		return err
	}
	if user.BotToken, err = rows.Text(8); err != nil {
		return err
	}
	*dst = user
	return nil
}

// SidebarRooms mirrors database.DB.SidebarRooms, appending to dst (pass
// dst[:0] to reuse a buffer; dst may be nil).
func (c *Conn) SidebarRooms(dst []SidebarRoom, user int64) ([]SidebarRoom, error) {
	h, err := c.acquire(querySidebarRooms)
	if err != nil {
		return dst, err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return dst, err
	}
	if err := h.st.BindInt64(1, user); err != nil {
		return dst, err
	}
	rows := Rows{stmt: h.st}
	out := dst
	for rows.Next() {
		var room SidebarRoom
		room.ID = rows.Int64(0)
		room.CreatorID = rows.Int64(1)
		if room.Name, err = rows.Text(2); err != nil {
			return out, err
		}
		if room.Type, err = rows.Text(3); err != nil {
			return out, err
		}
		if room.UpdatedAt, err = rows.Stamp(4); err != nil {
			return out, err
		}
		if room.Involvement, err = rows.Text(5); err != nil {
			return out, err
		}
		room.Unread = rows.Bool(6)
		out = append(out, room)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// MessageRefs mirrors the reduced scan of
// database.DB.MessagePageReferences for direction "before" (or anchor zero):
// up to 40 references in chronological order, appended to dst (pass dst[:0] to
// reuse a buffer). The full record path for after/around is MessagePage. This
// is the zero-allocation scan: with a sufficient caller buffer it performs no
// Go heap allocation.
func (c *Conn) MessageRefs(dst []MessageRef, room, anchor int64) ([]MessageRef, error) {
	query := queryRefsLatest
	if anchor != 0 {
		query = queryRefsBefore
	}
	h, err := c.acquire(query)
	if err != nil {
		return dst, err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return dst, err
	}
	if err := h.st.BindInt64(1, room); err != nil {
		return dst, err
	}
	if anchor != 0 {
		if err := h.st.BindInt64(2, anchor); err != nil {
			return dst, err
		}
		if err := h.st.BindInt64(3, room); err != nil {
			return dst, err
		}
	}
	rows := Rows{stmt: h.st}
	out := dst
	for rows.Next() {
		var ref MessageRef
		ref.ID = rows.Int64(0)
		ref.RoomID = room
		if ref.UpdatedAt, err = rows.Stamp(1); err != nil {
			return out, err
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// The SQL scans newest-first; internal/database reverses to chronological
	// order before returning. Only the appended segment is reversed.
	for i, j := len(dst), len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// MessagePageReferences mirrors database.DB.MessagePageReferences exactly:
// for direction "before" (or anchor zero) it returns the reduced references,
// and for after/around it delegates to MessagePage. Appends to dst.
func (c *Conn) MessagePageReferences(dst []Message, room, anchor int64, direction string) ([]Message, error) {
	if direction != "before" && anchor != 0 {
		return c.MessagePage(dst, room, anchor, direction)
	}
	query := queryRefsLatest
	if anchor != 0 {
		query = queryRefsBefore
	}
	h, err := c.acquire(query)
	if err != nil {
		return dst, err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return dst, err
	}
	if err := h.st.BindInt64(1, room); err != nil {
		return dst, err
	}
	if anchor != 0 {
		if err := h.st.BindInt64(2, anchor); err != nil {
			return dst, err
		}
		if err := h.st.BindInt64(3, room); err != nil {
			return dst, err
		}
	}
	rows := Rows{stmt: h.st}
	out := dst
	for rows.Next() {
		message := Message{RoomID: room}
		message.ID = rows.Int64(0)
		if message.UpdatedAt, err = rows.Stamp(1); err != nil {
			return out, err
		}
		out = append(out, message)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for i, j := len(dst), len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// MessagePage mirrors database.DB.MessagePage: the full records for the
// before/anchor-zero window, the after window, or the around window (before,
// anchor, after). Appends to dst (pass dst[:0] to reuse a buffer).
func (c *Conn) MessagePage(dst []Message, room, anchor int64, direction string) ([]Message, error) {
	if direction == "before" || anchor == 0 {
		return c.messages(dst, room, anchor)
	}
	stamp, err := c.anchorStamp(room, anchor)
	if err != nil {
		return dst, err
	}
	if direction == "after" {
		return c.messagesAfter(dst, room, stamp)
	}
	out, err := c.messages(dst, room, anchor)
	if err != nil {
		return out, err
	}
	if out, err = c.messageByID(out, room, anchor); err != nil {
		return out, err
	}
	return c.messagesAfter(out, room, stamp)
}

// messages mirrors database.DB.Messages: up to 40 full records ordered
// chronologically (the SQL scans newest-first and the result is reversed), for
// the room or the window before anchor when it is nonzero. Appends to dst.
func (c *Conn) messages(dst []Message, room, anchor int64) ([]Message, error) {
	query := queryMessagesLatest
	if anchor != 0 {
		query = queryMessagesBefore
	}
	h, err := c.acquire(query)
	if err != nil {
		return dst, err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return dst, err
	}
	if err := h.st.BindInt64(1, room); err != nil {
		return dst, err
	}
	if anchor != 0 {
		if err := h.st.BindInt64(2, anchor); err != nil {
			return dst, err
		}
		if err := h.st.BindInt64(3, room); err != nil {
			return dst, err
		}
	}
	rows := Rows{stmt: h.st}
	out := dst
	for rows.Next() {
		message, err := scanMessage(&rows)
		if err != nil {
			return out, err
		}
		out = append(out, message)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for i, j := len(dst), len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// messagesAfter appends the first 40 messages newer than the anchor stamp.
func (c *Conn) messagesAfter(dst []Message, room int64, stamp []byte) ([]Message, error) {
	h, err := c.acquire(queryMessagesAfter)
	if err != nil {
		return dst, err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return dst, err
	}
	if err := h.st.BindInt64(1, room); err != nil {
		return dst, err
	}
	if err := h.st.BindTextBytes(2, stamp); err != nil {
		return dst, err
	}
	rows := Rows{stmt: h.st}
	out := dst
	for rows.Next() {
		message, err := scanMessage(&rows)
		if err != nil {
			return out, err
		}
		out = append(out, message)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// messageByID appends the anchor's full record, mirroring MessagePage's
// centre lookup (a miss appends nothing).
func (c *Conn) messageByID(dst []Message, room, anchor int64) ([]Message, error) {
	h, err := c.acquire(queryMessageByID)
	if err != nil {
		return dst, err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return dst, err
	}
	if err := h.st.BindInt64(1, room); err != nil {
		return dst, err
	}
	if err := h.st.BindInt64(2, anchor); err != nil {
		return dst, err
	}
	rows := Rows{stmt: h.st}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return dst, err
		}
		return dst, nil
	}
	message, err := scanMessage(&rows)
	if err != nil {
		return dst, err
	}
	return append(dst, message), rows.Err()
}

// anchorStamp copies the anchor message's created_at text into the
// connection's scratch buffer and returns it. The bytes are valid until the
// next anchorStamp call; callers consume them immediately (bind).
func (c *Conn) anchorStamp(room, anchor int64) ([]byte, error) {
	h, err := c.acquire(queryAnchorStamp)
	if err != nil {
		return nil, err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return nil, err
	}
	if err := h.st.BindInt64(1, anchor); err != nil {
		return nil, err
	}
	if err := h.st.BindInt64(2, room); err != nil {
		return nil, err
	}
	rows := Rows{stmt: h.st}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrNoRows
	}
	if c.stamp, err = rows.ColumnTextInto(0, c.stamp[:0]); err != nil {
		return nil, err
	}
	return c.stamp, nil
}

// scanMessage decodes the eight messageSelect columns of the current row.
func scanMessage(rows *Rows) (Message, error) {
	var message Message
	message.ID = rows.Int64(0)
	message.RoomID = rows.Int64(1)
	message.CreatorID = rows.Int64(2)
	var err error
	if message.ClientID, err = rows.Text(3); err != nil {
		return message, err
	}
	if message.Body, err = rows.Text(4); err != nil {
		return message, err
	}
	if message.Creator, err = rows.Text(5); err != nil {
		return message, err
	}
	if message.CreatedAt, err = rows.Stamp(6); err != nil {
		return message, err
	}
	if message.UpdatedAt, err = rows.Stamp(7); err != nil {
		return message, err
	}
	return message, nil
}
