package database

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The reference's queries for rooms, messages, boosts, memberships and searches
// (reference/crates/db/src/models/{room,message,boost,membership,search,rich_text_record}.rs),
// with its SQL text.

const (
	membershipColumns = `"memberships"."id", "memberships"."room_id", "memberships"."user_id", "memberships"."involvement", "memberships"."unread_at", "memberships"."connected_at", "memberships"."connections", "memberships"."created_at", "memberships"."updated_at"`
	boostColumns      = `"boosts"."id", "boosts"."message_id", "boosts"."booster_id", "boosts"."content", "boosts"."created_at", "boosts"."updated_at"`
	richTextColumns   = `"action_text_rich_texts"."id", "action_text_rich_texts"."name", "action_text_rich_texts"."body", "action_text_rich_texts"."record_type", "action_text_rich_texts"."record_id", "action_text_rich_texts"."created_at", "action_text_rich_texts"."updated_at"`
	selectRoomUsers   = `SELECT ` + UserColumns + ` FROM "users" INNER JOIN "memberships" ON "users"."id" = "memberships"."user_id" WHERE "memberships"."room_id" = ?`
)

// ToDB is the reference's Timestamp::to_db, how it binds a time: seconds, then six digits of
// microseconds unless they are zero. A stored "…:00.000000" compares greater than a bound "…:00".
func ToDB(t time.Time) string {
	t = t.UTC()
	base := t.Format("2006-01-02 15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		return base + "." + fmt.Sprintf("%06d", us)
	}
	return base
}

// ReferenceMembership is a memberships row as the reference's Membership model reads it.
type ReferenceMembership struct {
	ID, RoomID, UserID int64
	// Nil when the column is NULL.
	Involvement *string
	UnreadAt    *time.Time
	UpdatedAt   time.Time
}

func scanReferenceMemberships(rows *Rows, err error) ([]ReferenceMembership, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var memberships []ReferenceMembership
	for rows.Next() {
		var m ReferenceMembership
		var involvement, unread, connected NullString
		var connections int64
		var created time.Time
		if err := rows.Scan(&m.ID, &m.RoomID, &m.UserID, &involvement, &unread, &connected, &connections, timestamp{&created}, timestamp{&m.UpdatedAt}); err != nil {
			return nil, err
		}
		if involvement.Valid {
			m.Involvement = &involvement.String
		}
		if unread.Valid {
			if t, ok := parseStamp(unread.String); ok {
				m.UnreadAt = &t
			}
		}
		memberships = append(memberships, m)
	}
	return memberships, rows.Err()
}

// MembershipFindByRoomAndUser is Membership::find_by_room_and_user.
func (d *DB) MembershipFindByRoomAndUser(ctx context.Context, room, user int64) (ReferenceMembership, bool, error) {
	memberships, err := scanReferenceMemberships(d.Read.QueryContext(ctx, `SELECT `+membershipColumns+` FROM "memberships" WHERE "memberships"."room_id" = ? AND "memberships"."user_id" = ? LIMIT 1`, room, user))
	if err != nil || len(memberships) == 0 {
		return ReferenceMembership{}, false, err
	}
	return memberships[0], true, nil
}

// MembershipsForRoom is Membership::for_room.
func (d *DB) MembershipsForRoom(ctx context.Context, room int64) ([]ReferenceMembership, error) {
	return scanReferenceMemberships(d.Read.QueryContext(ctx, `SELECT `+membershipColumns+` FROM "memberships" WHERE "memberships"."room_id" = ?`, room))
}

// UpdateMembershipInvolvement is Membership#update_involvement: nothing when it's unchanged.
func (d *DB) UpdateMembershipInvolvement(ctx context.Context, membership ReferenceMembership, involvement *string) error {
	if (membership.Involvement == nil) == (involvement == nil) && (involvement == nil || *membership.Involvement == *involvement) {
		return nil
	}
	return d.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE "memberships" SET "involvement" = ?, "updated_at" = ? WHERE "memberships"."id" = ?`, involvement, Stamp(d.Now()), membership.ID)
		return err
	})
}

// RoomFindByID is Room::find_by_id.
func (d *DB) RoomFindByID(ctx context.Context, id int64) (ReferenceRoom, bool, error) {
	return d.optionalRoom(ctx, `SELECT `+roomColumns+` FROM "rooms" WHERE "rooms"."id" = ? LIMIT 1`, id)
}

func (d *DB) referenceUsers(ctx context.Context, query string, args ...any) ([]User, error) {
	rows, err := d.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := ScanReferenceUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UserAuthenticateBot is User::authenticate_bot: the active bot with the key's id and token
// (split on "-" as Ruby does, so a key without a token finds nothing).
func (d *DB) UserAuthenticateBot(ctx context.Context, key string) (User, bool, error) {
	parts := strings.Split(key, "-")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 {
		return User{}, false, nil
	}
	users, err := d.referenceUsers(ctx, `SELECT `+UserColumns+` FROM "users" WHERE "users"."status" = 0 AND "users"."role" = 2 AND "users"."id" = ? AND "users"."bot_token" = ? LIMIT 1`, parts[0], parts[1])
	if err != nil || len(users) == 0 {
		return User{}, false, err
	}
	return users[0], true, nil
}

// RoomUsers is room.users.
func (d *DB) RoomUsers(ctx context.Context, room int64) ([]User, error) {
	return d.referenceUsers(ctx, selectRoomUsers, room)
}

// RoomActiveBots is room.users.active_bots.
func (d *DB) RoomActiveBots(ctx context.Context, room int64) ([]User, error) {
	return d.referenceUsers(ctx, selectRoomUsers+` AND "users"."status" = 0 AND "users"."role" = 2`, room)
}

// MentioneesInRoom is room.users.where(id: ids).
func (d *DB) MentioneesInRoom(ctx context.Context, room int64, ids []int64) ([]User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, room)
	for _, id := range ids {
		args = append(args, id)
	}
	return d.referenceUsers(ctx, `SELECT `+UserColumns+` FROM "users" INNER JOIN "memberships" ON "users"."id" = "memberships"."user_id" WHERE "memberships"."room_id" = ? AND "users"."id" IN (`+placeholders(len(ids))+`)`, args...)
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func (d *DB) optionalMessage(ctx context.Context, query string, args ...any) (ReferenceMessage, bool, error) {
	messages, err := d.referenceMessages(ctx, query, args...)
	if err != nil || len(messages) == 0 {
		return ReferenceMessage{}, false, err
	}
	return messages[0], true, nil
}

// MessageFindInRoom is room.messages.find(id), nil when it isn't there.
func (d *DB) MessageFindInRoom(ctx context.Context, room, id int64) (ReferenceMessage, bool, error) {
	return d.optionalMessage(ctx, selectInRoom+` AND "messages"."id" = ? LIMIT 1`, room, id)
}

// MessageFindReachable is Current.user.reachable_messages.find(id), nil when it isn't there.
func (d *DB) MessageFindReachable(ctx context.Context, user, id int64) (ReferenceMessage, bool, error) {
	return d.optionalMessage(ctx, selectReachable+` WHERE "memberships"."user_id" = ? AND "messages"."id" = ? LIMIT 1`, user, id)
}

// MessagePageCreatedSince is room.messages.page_created_since(time).
func (d *DB) MessagePageCreatedSince(ctx context.Context, room int64, since string) ([]ReferenceMessage, error) {
	return d.referenceMessages(ctx, selectInRoom+` AND (created_at > ?) ORDER BY "messages"."created_at" ASC LIMIT 40`, room, since)
}

// MessagePageUpdatedSince is room.messages.without(excluding).page_updated_since(time).
func (d *DB) MessagePageUpdatedSince(ctx context.Context, room int64, since string, excluding []int64) ([]ReferenceMessage, error) {
	without := ""
	args := make([]any, 0, len(excluding)+2)
	args = append(args, room)
	if len(excluding) > 0 {
		without = ` AND "messages"."id" NOT IN (` + placeholders(len(excluding)) + `)`
		for _, id := range excluding {
			args = append(args, id)
		}
	}
	args = append(args, since)
	messages, err := d.referenceMessages(ctx, selectInRoom+without+` AND (updated_at > ?) ORDER BY "messages"."created_at" DESC LIMIT 40`, args...)
	return reversed(messages), err
}

// MessageCountInRoom is room.messages.count.
func (d *DB) MessageCountInRoom(ctx context.Context, room int64) (int64, error) {
	var count int64
	err := d.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM "messages" WHERE "messages"."room_id" = ?`, room).Scan(&count)
	return count, err
}

func (d *DB) exists(ctx context.Context, query string, args ...any) (bool, error) {
	var one int64
	err := d.Read.QueryRowContext(ctx, query, args...).Scan(&one)
	if err == ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// MessageExistsBefore is room.messages.before(message).exists?.
func (d *DB) MessageExistsBefore(ctx context.Context, room int64, message ReferenceMessage) (bool, error) {
	return d.exists(ctx, `SELECT 1 FROM "messages" WHERE "messages"."room_id" = ? AND (created_at < ?) LIMIT 1`, room, ToDB(message.CreatedAt))
}

// MessageExistsAfter is room.messages.after(message).exists?.
func (d *DB) MessageExistsAfter(ctx context.Context, room int64, message ReferenceMessage) (bool, error) {
	return d.exists(ctx, `SELECT 1 FROM "messages" WHERE "messages"."room_id" = ? AND (created_at > ?) LIMIT 1`, room, ToDB(message.CreatedAt))
}

// MessageBodyHTML is message.body.body: RichTextRecord::find_for(conn, "Message", id, "body"),
// nil without a record or with a NULL body.
func (d *DB) MessageBodyHTML(ctx context.Context, message int64) (*string, error) {
	var id, recordID int64
	var name, recordType string
	var body NullString
	var created, updated time.Time
	err := d.Read.QueryRowContext(ctx, `SELECT `+richTextColumns+` FROM "action_text_rich_texts" WHERE "action_text_rich_texts"."record_id" = ? AND "action_text_rich_texts"."record_type" = ? AND "action_text_rich_texts"."name" = ? LIMIT 1`, message, "Message", "body").
		Scan(&id, &name, &body, &recordType, &recordID, timestamp{&created}, timestamp{&updated})
	if err == ErrNoRows || err == nil && !body.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &body.String, nil
}

// ReferenceBoost is a boosts row as the reference's Boost model reads it.
type ReferenceBoost struct {
	ID, MessageID, BoosterID int64
	Content                  string
	CreatedAt, UpdatedAt     time.Time
}

func (d *DB) referenceBoosts(ctx context.Context, query string, args ...any) ([]ReferenceBoost, error) {
	rows, err := d.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var boosts []ReferenceBoost
	for rows.Next() {
		var b ReferenceBoost
		if err := rows.Scan(&b.ID, &b.MessageID, &b.BoosterID, &b.Content, timestamp{&b.CreatedAt}, timestamp{&b.UpdatedAt}); err != nil {
			return nil, err
		}
		boosts = append(boosts, b)
	}
	return boosts, rows.Err()
}

// BoostsForMessageOrdered is message.boosts.ordered.
func (d *DB) BoostsForMessageOrdered(ctx context.Context, message int64) ([]ReferenceBoost, error) {
	return d.referenceBoosts(ctx, `SELECT `+boostColumns+` FROM "boosts" WHERE "boosts"."message_id" = ? ORDER BY "boosts"."created_at" ASC`, message)
}

// BoostFindByMessageAndBooster is message.boosts.find_by(id:, booster:).
func (d *DB) BoostFindByMessageAndBooster(ctx context.Context, message, id, booster int64) (ReferenceBoost, bool, error) {
	boosts, err := d.referenceBoosts(ctx, `SELECT `+boostColumns+` FROM "boosts" WHERE "boosts"."message_id" = ? AND "boosts"."id" = ? AND "boosts"."booster_id" = ? LIMIT 1`, message, id, booster)
	if err != nil || len(boosts) == 0 {
		return ReferenceBoost{}, false, err
	}
	return boosts[0], true, nil
}

// BotsWithWebhooks is each bot's deliver_webhook_later(message) check, Webhook::find_by_user, in
// one write: the bots that have a webhook.
func (d *DB) BotsWithWebhooks(ctx context.Context, bots []int64) ([]int64, error) {
	var withWebhooks []int64
	err := d.Transaction(ctx, func(tx *Tx) error {
		for _, bot := range bots {
			rows, err := tx.QueryContext(ctx, `SELECT "webhooks".* FROM "webhooks" WHERE "webhooks"."user_id" = ? LIMIT 1`, bot)
			if err != nil {
				return err
			}
			if rows.Next() {
				withWebhooks = append(withWebhooks, bot)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	return withWebhooks, err
}

// selectStarColumn runs a `SELECT *` query (read by column name, as the reference's from_row does)
// and returns the named column of its first row, if any.
func (d *DB) selectStarColumn(ctx context.Context, column, query string, args ...any) (string, bool, error) {
	rows, err := d.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", false, rows.Err()
	}
	for i := range rows.stmt.ColumnCount() {
		if rows.stmt.ColumnName(i) == column {
			return rows.stmt.ColumnText(i), true, nil
		}
	}
	return "", true, nil
}

// MessageAttachmentFilename is message.attachment&.filename: Attachment::find_for, then the
// attachment's Blob::find (a missing blob is ErrNoRows, RecordNotFound).
func (d *DB) MessageAttachmentFilename(ctx context.Context, message int64) (string, bool, error) {
	blobID, found, err := d.selectStarColumn(ctx, "blob_id", `SELECT * FROM "active_storage_attachments" WHERE "active_storage_attachments"."record_id" = ? AND "active_storage_attachments"."record_type" = ? AND "active_storage_attachments"."name" = ? LIMIT 1`, message, "Message", "attachment")
	if err != nil || !found {
		return "", false, err
	}
	filename, found, err := d.selectStarColumn(ctx, "filename", `SELECT * FROM "active_storage_blobs" WHERE "active_storage_blobs"."id" = ? LIMIT 1`, blobID)
	if err == nil && !found {
		err = ErrNoRows
	}
	return filename, err == nil, err
}

// SearchQueriesOrderedForUser is user.searches.ordered.pluck(:query), through the reference's
// `SELECT "searches".*` (read by column name: migrated databases order the columns differently).
func (d *DB) SearchQueriesOrderedForUser(ctx context.Context, user int64) ([]string, error) {
	rows, err := d.Read.QueryContext(ctx, `SELECT "searches".* FROM "searches" WHERE "searches"."user_id" = ? ORDER BY "searches"."updated_at" DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	queries := []string{}
	column := -1
	for rows.Next() {
		if column < 0 {
			for i := range rows.stmt.ColumnCount() {
				if rows.stmt.ColumnName(i) == "query" {
					column = i
				}
			}
		}
		queries = append(queries, rows.stmt.ColumnText(column))
	}
	return queries, rows.Err()
}

// SearchRecord is Search::record: user.searches.find_or_create_by(query:).touch; creating trims
// the user's searches to the ten most recent.
func (d *DB) SearchRecord(ctx context.Context, user int64, query string) error {
	return d.Transaction(ctx, func(tx *Tx) error {
		now := Stamp(d.Now())
		var id int64
		rows, err := tx.QueryContext(ctx, `SELECT "searches".* FROM "searches" WHERE "searches"."user_id" = ? AND "searches"."query" = ? LIMIT 1`, user, query)
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			for i := range rows.stmt.ColumnCount() {
				if rows.stmt.ColumnName(i) == "id" {
					id, found = rows.stmt.ColumnInt64(i), true
				}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !found {
			if err := tx.QueryRowContext(ctx, `INSERT INTO "searches" ("created_at", "query", "updated_at", "user_id") VALUES (?, ?, ?, ?) RETURNING "id"`, now, query, now, user).Scan(&id); err != nil {
				return err
			}
			if err := trimRecentSearches(ctx, tx, user); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE "searches" SET "updated_at" = ? WHERE "searches"."id" = ?`, now, id)
		return err
	})
}

func searchIDs(ctx context.Context, tx *Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func deleteSearches(ctx context.Context, tx *Tx, ids []int64) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM "searches" WHERE "searches"."id" = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// trimRecentSearches is user.searches.excluding(user.searches.ordered.limit(10)).destroy_all.
func trimRecentSearches(ctx context.Context, tx *Tx, user int64) error {
	keep, err := searchIDs(ctx, tx, `SELECT "searches"."id" FROM "searches" WHERE "searches"."user_id" = ? ORDER BY "searches"."updated_at" DESC LIMIT ?`, user, int64(10))
	if err != nil {
		return err
	}
	args := []any{user}
	if len(keep) == 0 {
		args = append(args, int64(0))
	}
	for _, id := range keep {
		args = append(args, id)
	}
	doomed, err := searchIDs(ctx, tx, `SELECT "searches"."id" FROM "searches" WHERE "searches"."user_id" = ? AND "searches"."id" NOT IN (`+placeholders(max(len(keep), 1))+`)`, args...)
	if err != nil {
		return err
	}
	return deleteSearches(ctx, tx, doomed)
}

// SearchDestroyAllForUser is user.searches.destroy_all.
func (d *DB) SearchDestroyAllForUser(ctx context.Context, user int64) error {
	return d.Transaction(ctx, func(tx *Tx) error {
		ids, err := searchIDs(ctx, tx, `SELECT "searches"."id" FROM "searches" WHERE "searches"."user_id" = ?`, user)
		if err != nil {
			return err
		}
		return deleteSearches(ctx, tx, ids)
	})
}
