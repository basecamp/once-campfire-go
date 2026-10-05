package database

import (
	"context"
	"strings"
	"time"
)

// The reference's queries for rooms, messages, boosts, memberships and searches
// (reference/crates/db/src/models/{room,message,boost,membership,search,rich_text_record}.rs),
// with its SQL text.

const (
	boostColumns    = `"boosts"."id", "boosts"."message_id", "boosts"."booster_id", "boosts"."content", "boosts"."created_at", "boosts"."updated_at"`
	richTextColumns = `"action_text_rich_texts"."id", "action_text_rich_texts"."name", "action_text_rich_texts"."body", "action_text_rich_texts"."record_type", "action_text_rich_texts"."record_id", "action_text_rich_texts"."created_at", "action_text_rich_texts"."updated_at"`
)

// MembershipFindByRoomAndUser is Membership::find_by_room_and_user.
func (d *DB) MembershipFindByRoomAndUser(ctx context.Context, room, user int64) (ReferenceMembership, bool, error) {
	var m ReferenceMembership
	var unread, connected NullString
	var connections int64
	err := d.Read.QueryRowContext(ctx, `SELECT `+membershipColumns+` FROM "memberships" WHERE "memberships"."room_id" = ? AND "memberships"."user_id" = ? LIMIT 1`, room, user).
		Scan(&m.ID, &m.RoomID, &m.UserID, &m.Involvement, &unread, &connected, &connections, timestamp{&m.CreatedAt}, timestamp{&m.UpdatedAt})
	if err == ErrNoRows {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	m.Unread = unread.Valid
	if unread.Valid {
		var t time.Time
		if err := (timestamp{&t}).parse(unread.String); err != nil {
			return m, false, err
		}
		m.UnreadAt = &t
	}
	return m, true, nil
}

// UpdateMembershipInvolvement is Membership#update_involvement: nothing when it's unchanged.
func (d *DB) UpdateMembershipInvolvement(ctx context.Context, membership ReferenceMembership, involvement *string) error {
	if membership.Involvement.Valid == (involvement != nil) && (involvement == nil || membership.Involvement.String == *involvement) {
		return nil
	}
	return d.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE "memberships" SET "involvement" = ?, "updated_at" = ? WHERE "memberships"."id" = ?`, involvement, Stamp(d.Now()), membership.ID)
		return err
	})
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
	return d.optionalUser(ctx, selectUsers+` WHERE "users"."status" = 0 AND "users"."role" = 2 AND "users"."id" = ? AND "users"."bot_token" = ? LIMIT 1`, parts[0], parts[1])
}

// RoomActiveBots is room.users.active_bots.
func (d *DB) RoomActiveBots(ctx context.Context, room int64) ([]User, error) {
	return d.referenceUsers(ctx, `SELECT `+UserColumns+roomUsers+` AND "users"."status" = 0 AND "users"."role" = 2`, room)
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
	return d.exists(ctx, `SELECT 1 FROM "messages" WHERE "messages"."room_id" = ? AND (created_at < ?) LIMIT 1`, room, Stamp(message.CreatedAt))
}

// MessageExistsAfter is room.messages.after(message).exists?.
func (d *DB) MessageExistsAfter(ctx context.Context, room int64, message ReferenceMessage) (bool, error) {
	return d.exists(ctx, `SELECT 1 FROM "messages" WHERE "messages"."room_id" = ? AND (created_at > ?) LIMIT 1`, room, Stamp(message.CreatedAt))
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
