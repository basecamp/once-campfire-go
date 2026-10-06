package database

import (
	"context"
	"strings"
	"time"
	"unicode"
)

// The reference's model queries (reference/crates/db/src/models, reference/crates/storage), with
// the reference's SQL text, so that a page runs the same statements in both applications. Column
// lists are what the reference's `columns!` macro expands to.

const (
	accountColumns = `"accounts"."id", "accounts"."name", "accounts"."join_code", "accounts"."custom_styles", "accounts"."settings", "accounts"."singleton_guard", "accounts"."created_at", "accounts"."updated_at"`
	roomColumns    = `"rooms"."id", "rooms"."name", "rooms"."type", "rooms"."creator_id", "rooms"."created_at", "rooms"."updated_at"`
	selectForUser  = `SELECT ` + roomColumns + ` FROM "rooms" INNER JOIN "memberships" ON "rooms"."id" = "memberships"."room_id" WHERE "memberships"."user_id" = ?`
	blobColumns    = "b.id, b.key, b.filename, b.content_type, b.metadata, b.service_name, b.byte_size, b.checksum, b.created_at"
)

// AccountFirst is Account::first, with the account's logo left to AttachedBlob.
func (d *DB) AccountFirst(ctx context.Context) (Account, bool, error) {
	var a Account
	var customStyles, settings NullString
	var guard int64
	var created time.Time
	err := d.Read.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM "accounts" ORDER BY "accounts"."id" ASC LIMIT 1`).
		Scan(&a.ID, &a.Name, &a.JoinCode, &customStyles, &settings, &guard, timestamp{&created}, timestamp{&a.UpdatedAt})
	if err == ErrNoRows {
		return Account{}, false, nil
	}
	a.CustomStyles, a.HasCustomStyles = customStyles.String, customStyles.Valid
	a.Settings = []byte(settings.String)
	if !settings.Valid {
		a.Settings = []byte("{}")
	}
	return a, err == nil, err
}

// Blob is an Active Storage blob row, as Blob::attached reads it.
type Blob struct {
	ID                         int64
	Key, Filename, ServiceName string
	ContentType, Metadata      NullString
	ByteSize                   int64
	Checksum                   NullString
	CreatedAt                  string
}

// AttachedBlob is Blob::attached: the blob attached to a record as name, if any.
func (d *DB) AttachedBlob(ctx context.Context, recordType string, recordID int64, name string) (Blob, bool, error) {
	var b Blob
	err := d.Read.QueryRowContext(ctx, "SELECT "+blobColumns+" FROM active_storage_blobs b JOIN active_storage_attachments a ON a.blob_id = b.id WHERE a.record_type = ?1 AND a.record_id = ?2 AND a.name = ?3 ORDER BY a.id LIMIT 1", recordType, recordID, name).
		Scan(&b.ID, &b.Key, &b.Filename, &b.ContentType, &b.Metadata, &b.ServiceName, &b.ByteSize, &b.Checksum, &b.CreatedAt)
	if err == ErrNoRows {
		return Blob{}, false, nil
	}
	return b, err == nil, err
}

// ReferenceRoom is a room row as the reference's Room model reads it.
type ReferenceRoom struct {
	ID, CreatorID        int64
	Name                 NullString
	Type                 string
	CreatedAt, UpdatedAt time.Time
}

func scanReferenceRoom(row interface{ Scan(...any) error }) (ReferenceRoom, error) {
	var r ReferenceRoom
	err := row.Scan(&r.ID, &r.Name, &r.Type, &r.CreatorID, timestamp{&r.CreatedAt}, timestamp{&r.UpdatedAt})
	return r, err
}

func (d *DB) optionalRoom(ctx context.Context, query string, args ...any) (ReferenceRoom, bool, error) {
	room, err := scanReferenceRoom(d.Read.QueryRowContext(ctx, query, args...))
	if err == ErrNoRows {
		return ReferenceRoom{}, false, nil
	}
	return room, err == nil, err
}

// RoomForUser is Room::find_for_user: one of the user's rooms.
func (d *DB) RoomForUser(ctx context.Context, user, room int64) (ReferenceRoom, bool, error) {
	return d.optionalRoom(ctx, selectForUser+` AND "rooms"."id" = ? LIMIT 1`, user, room)
}

// OriginalRoomForUser is Room::original_for_user: `Current.user.rooms.original`.
func (d *DB) OriginalRoomForUser(ctx context.Context, user int64) (ReferenceRoom, bool, error) {
	return d.optionalRoom(ctx, selectForUser+` ORDER BY "rooms"."created_at" ASC LIMIT 1`, user)
}

// FirstRoom is Room::original: the first room created.
func (d *DB) FirstRoom(ctx context.Context) (ReferenceRoom, bool, error) {
	return d.optionalRoom(ctx, `SELECT `+roomColumns+` FROM "rooms" ORDER BY "rooms"."created_at" ASC LIMIT 1`)
}

const (
	messageColumns  = `"messages"."id", "messages"."room_id", "messages"."creator_id", "messages"."client_message_id", "messages"."created_at", "messages"."updated_at"`
	selectInRoom    = `SELECT ` + messageColumns + ` FROM "messages" WHERE "messages"."room_id" = ?`
	selectReachable = `SELECT ` + messageColumns + ` FROM "messages" INNER JOIN "rooms" ON "messages"."room_id" = "rooms"."id" INNER JOIN "memberships" ON "rooms"."id" = "memberships"."room_id"`
)

// ReferenceMessage is a message row as the reference's Message model reads it (no body).
type ReferenceMessage struct {
	ID, RoomID, CreatorID int64
	ClientMessageID       string
	CreatedAt, UpdatedAt  time.Time
}

// referenceMessages reads messages into a slice sized for the rows expected (a page's LIMIT),
// scanning each row in place.
func (d *DB) referenceMessages(ctx context.Context, expected int, query string, args ...any) ([]ReferenceMessage, error) {
	rows, err := d.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]ReferenceMessage, 0, expected)
	for rows.Next() {
		messages = append(messages, ReferenceMessage{})
		m := &messages[len(messages)-1]
		if err := rows.Scan(&m.ID, &m.RoomID, &m.CreatorID, &m.ClientMessageID, timestamp{&m.CreatedAt}, timestamp{&m.UpdatedAt}); err != nil {
			return nil, err
		}
	}
	return messages, rows.Err()
}

func reversed(messages []ReferenceMessage) []ReferenceMessage {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages
}

// MessageFindByID is Message::find_by_id.
func (d *DB) MessageFindByID(ctx context.Context, id int64) (ReferenceMessage, bool, error) {
	messages, err := d.referenceMessages(ctx, 1, `SELECT `+messageColumns+` FROM "messages" WHERE "messages"."id" = ? LIMIT 1`, id)
	if err != nil || len(messages) == 0 {
		return ReferenceMessage{}, false, err
	}
	return messages[0], true, nil
}

// MessageLastPage is room.messages.last_page: the newest 40, oldest first.
func (d *DB) MessageLastPage(ctx context.Context, room int64) ([]ReferenceMessage, error) {
	messages, err := d.referenceMessages(ctx, 40, selectInRoom+` ORDER BY "messages"."created_at" DESC LIMIT 40`, room)
	return reversed(messages), err
}

// MessagePageBefore is room.messages.page_before(message).
func (d *DB) MessagePageBefore(ctx context.Context, room int64, message ReferenceMessage) ([]ReferenceMessage, error) {
	messages, err := d.referenceMessages(ctx, 40, selectInRoom+` AND (created_at < ?) ORDER BY "messages"."created_at" DESC LIMIT 40`, room, Stamp(message.CreatedAt))
	return reversed(messages), err
}

// MessagePageAfter is room.messages.page_after(message).
func (d *DB) MessagePageAfter(ctx context.Context, room int64, message ReferenceMessage) ([]ReferenceMessage, error) {
	return d.referenceMessages(ctx, 40, selectInRoom+` AND (created_at > ?) ORDER BY "messages"."created_at" ASC LIMIT 40`, room, Stamp(message.CreatedAt))
}

// MessagePageAround is room.messages.page_around(message): up to 40 before, the message, up to
// 40 after.
func (d *DB) MessagePageAround(ctx context.Context, room int64, message ReferenceMessage) ([]ReferenceMessage, error) {
	page, err := d.MessagePageBefore(ctx, room, message)
	if err != nil {
		return nil, err
	}
	page = append(page, message)
	after, err := d.MessagePageAfter(ctx, room, message)
	return append(page, after...), err
}

// MessagePaged is room.messages.paged?: whether a row exists past the first page.
func (d *DB) MessagePaged(ctx context.Context, room int64) (bool, error) {
	var one int64
	err := d.Read.QueryRowContext(ctx, `SELECT 1 FROM "messages" WHERE "messages"."room_id" = ? LIMIT 1 OFFSET 40`, room).Scan(&one)
	if err == ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// MessageSearchReachable is `Current.user.reachable_messages.search(query).last(100)`.
func (d *DB) MessageSearchReachable(ctx context.Context, user int64, query string) ([]ReferenceMessage, error) {
	terms := matchTerms(query)
	if terms == "" {
		return nil, nil
	}
	// Searches mostly find fewer messages than the LIMIT.
	messages, err := d.referenceMessages(ctx, 16, selectReachable+` join message_search_index idx on messages.id = idx.rowid WHERE "memberships"."user_id" = ? AND (idx.body match ?) ORDER BY "messages"."created_at" DESC LIMIT 100`, user, terms)
	return reversed(messages), err
}

// matchTerms is each word of a search as an FTS5 string, so every word must appear and none is
// read as query syntax.
func matchTerms(query string) string {
	var words []string
	for _, word := range strings.FieldsFunc(query, func(c rune) bool { return unicode.IsSpace(c) || c == 0 }) {
		words = append(words, `"`+strings.ReplaceAll(word, `"`, `""`)+`"`)
	}
	return strings.Join(words, " ")
}

const (
	sessionColumns = `"sessions"."id", "sessions"."user_id", "sessions"."token", "sessions"."ip_address", "sessions"."user_agent", "sessions"."last_active_at", "sessions"."created_at", "sessions"."updated_at"`
	// UserColumns are the reference's users columns, in its order.
	UserColumns = `"users"."id", "users"."name", "users"."email_address", "users"."password_digest", "users"."role", "users"."status", "users"."bio", "users"."bot_token", "users"."created_at", "users"."updated_at"`
	// Session::ACTIVITY_REFRESH_RATE.
	activityRefreshRate = time.Hour
)

// ReferenceSession is a sessions row as the reference's Session model reads it.
type ReferenceSession struct {
	ID, UserID           int64
	Token                string
	IPAddress, UserAgent NullString
	LastActiveAt         time.Time
}

// SessionByToken is Session.find_by(token:).
func (d *DB) SessionByToken(ctx context.Context, token string) (ReferenceSession, bool, error) {
	var s ReferenceSession
	var created, updated time.Time
	err := d.Read.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM "sessions" WHERE "sessions"."token" = ? LIMIT 1`, token).
		Scan(&s.ID, &s.UserID, &s.Token, &s.IPAddress, &s.UserAgent, timestamp{&s.LastActiveAt}, timestamp{&created}, timestamp{&updated})
	if err == ErrNoRows {
		return ReferenceSession{}, false, nil
	}
	return s, err == nil, err
}

// NeedsResume is Session#needs_resume: its activity is older than the refresh rate.
func (s ReferenceSession) NeedsResume(now time.Time) bool {
	return s.LastActiveAt.Before(now.Add(-activityRefreshRate))
}

// ResumeSession is Session#resume: refreshes activity, user agent and IP at most once an hour.
func (d *DB) ResumeSession(ctx context.Context, session ReferenceSession, userAgent, ip *string) error {
	return d.Transaction(ctx, func(tx *Tx) error {
		now := d.Now()
		if !session.NeedsResume(now) {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE "sessions" SET "ip_address" = ?, "last_active_at" = ?, "updated_at" = ?, "user_agent" = ? WHERE "sessions"."id" = ?`, ip, Stamp(now), Stamp(now), userAgent, session.ID)
		return err
	})
}

// ScanReferenceUser reads a row of UserColumns.
func ScanReferenceUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var email, digest, bio, botToken NullString
	var created time.Time
	err := row.Scan(&u.ID, &u.Name, &email, &digest, &u.Role, &u.Status, &bio, &botToken, timestamp{&created}, timestamp{&u.UpdatedAt})
	u.Email, u.Password, u.Bio, u.BotToken = email.String, digest.String, bio.String, botToken.String
	u.NullEmail, u.NullBio = !email.Valid, !bio.Valid
	return u, err
}

// UserFindByID is User::find_by_id.
func (d *DB) UserFindByID(ctx context.Context, id int64) (User, bool, error) {
	u, err := ScanReferenceUser(d.Read.QueryRowContext(ctx, `SELECT `+UserColumns+` FROM "users" WHERE "users"."id" = ? LIMIT 1`, id))
	if err == ErrNoRows {
		return User{}, false, nil
	}
	return u, err == nil, err
}
