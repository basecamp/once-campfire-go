package database

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// The reference's model queries behind the account settings, bots and room forms
// (reference/crates/db/src/models/{account,user,room,webhook}.rs), with its SQL text.

const (
	selectUsers = `SELECT ` + UserColumns + ` FROM "users"`
	// SQLITE_NOW: memberships inserted in bulk take the database's clock.
	sqliteNow = `STRFTIME('%Y-%m-%d %H:%M:%f', 'NOW')`
)

func (d *DB) referenceUsers(ctx context.Context, query string, args ...any) ([]User, error) {
	rows, err := d.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanReferenceUsers(rows)
}

func scanReferenceUsers(rows *Rows) ([]User, error) {
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

func (d *DB) optionalUser(ctx context.Context, query string, args ...any) (User, bool, error) {
	u, err := ScanReferenceUser(d.Read.QueryRowContext(ctx, query, args...))
	if err == ErrNoRows {
		return User{}, false, nil
	}
	return u, err == nil, err
}

// UsersForAccount is AccountsController#account_users: `User.where(status: [ :active, :banned ])`
// for administrators, `User.active` otherwise, `.ordered.without_bots`.
func (d *DB) UsersForAccount(ctx context.Context, canAdminister bool) ([]User, error) {
	status := `"users"."status" = 0`
	if canAdminister {
		status = `"users"."status" IN (0, 2)`
	}
	return d.referenceUsers(ctx, selectUsers+` WHERE `+status+` AND "users"."role" != 2 ORDER BY LOWER(name)`)
}

// UsersActiveOrdered is `User.active.ordered`.
func (d *DB) UsersActiveOrdered(ctx context.Context) ([]User, error) {
	return d.referenceUsers(ctx, selectUsers+` WHERE "users"."status" = 0 ORDER BY LOWER(name)`)
}

// UsersActiveOrderedWithoutBots is `User.active.ordered.without_bots`.
func (d *DB) UsersActiveOrderedWithoutBots(ctx context.Context) ([]User, error) {
	return d.referenceUsers(ctx, selectUsers+` WHERE "users"."status" = 0 AND "users"."role" != 2 ORDER BY LOWER(name)`)
}

// UsersActiveBotsOrdered is `User.active_bots.ordered`.
func (d *DB) UsersActiveBotsOrdered(ctx context.Context) ([]User, error) {
	return d.referenceUsers(ctx, selectUsers+` WHERE "users"."status" = 0 AND "users"."role" = 2 ORDER BY LOWER(name)`)
}

// UserFindActive is `User.active.find(id)`.
func (d *DB) UserFindActive(ctx context.Context, id int64) (User, bool, error) {
	return d.optionalUser(ctx, selectUsers+` WHERE "users"."status" = 0 AND "users"."id" = ? LIMIT 1`, id)
}

// UserFindActiveBot is `User.active_bots.find(id)`.
func (d *DB) UserFindActiveBot(ctx context.Context, id int64) (User, bool, error) {
	return d.optionalUser(ctx, selectUsers+` WHERE "users"."status" = 0 AND "users"."role" = 2 AND "users"."id" = ? LIMIT 1`, id)
}

// UserWebhookURL is `user.webhook&.url`.
func (d *DB) UserWebhookURL(ctx context.Context, user int64) (*string, error) {
	var id, userID int64
	var url NullString
	var created, updated string
	err := d.Read.QueryRowContext(ctx, `SELECT "webhooks".* FROM "webhooks" WHERE "webhooks"."user_id" = ? LIMIT 1`, user).
		Scan(&id, &created, &updated, &url, &userID)
	if err == ErrNoRows || err == nil && !url.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &url.String, nil
}

func (d *DB) referenceRooms(ctx context.Context, query string, args ...any) ([]ReferenceRoom, error) {
	rows, err := d.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rooms []ReferenceRoom
	for rows.Next() {
		room, err := scanReferenceRoom(rows)
		if err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	return rooms, rows.Err()
}

// RoomsForUserWithoutDirects is `user.rooms.without_directs`.
func (d *DB) RoomsForUserWithoutDirects(ctx context.Context, user int64) ([]ReferenceRoom, error) {
	return d.referenceRooms(ctx, selectForUser+` AND "rooms"."type" != ?`, user, "Rooms::Direct")
}

// RoomFind is Room::find.
func (d *DB) RoomFind(ctx context.Context, id int64) (ReferenceRoom, error) {
	return scanReferenceRoom(d.Read.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM "rooms" WHERE "rooms"."id" = ? LIMIT 1`, id))
}

const (
	roomUsers   = ` FROM "users" INNER JOIN "memberships" ON "users"."id" = "memberships"."user_id" WHERE "memberships"."room_id" = ?`
	roomUserIDs = `SELECT "users"."id"` + roomUsers
)

// RoomUsers is `room.users`.
func (d *DB) RoomUsers(ctx context.Context, room int64) ([]User, error) {
	return d.referenceUsers(ctx, `SELECT `+UserColumns+roomUsers, room)
}

// RoomUserIDs is `room.user_ids`.
func (d *DB) RoomUserIDs(ctx context.Context, room int64) ([]int64, error) {
	rows, err := d.Read.QueryContext(ctx, roomUserIDs, room)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

func scanIDs(rows *Rows) ([]int64, error) {
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

// ReferenceMembership is a memberships row as the reference's Membership model reads it.
type ReferenceMembership struct {
	ID, RoomID, UserID int64
	// Nullable (default "mentions").
	Involvement NullString
	UnreadAt    *time.Time
	// unread_at IS NOT NULL (Membership#unread?).
	Unread               bool
	CreatedAt, UpdatedAt time.Time
}

// MembershipsForRoom is Membership::for_room.
func (d *DB) MembershipsForRoom(ctx context.Context, room int64) ([]ReferenceMembership, error) {
	rows, err := d.Read.QueryContext(ctx, `SELECT "memberships"."id", "memberships"."room_id", "memberships"."user_id", "memberships"."involvement", "memberships"."unread_at", "memberships"."connected_at", "memberships"."connections", "memberships"."created_at", "memberships"."updated_at" FROM "memberships" WHERE "memberships"."room_id" = ?`, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var memberships []ReferenceMembership
	for rows.Next() {
		var m ReferenceMembership
		var unread, connected NullString
		var connections int64
		if err := rows.Scan(&m.ID, &m.RoomID, &m.UserID, &m.Involvement, &unread, &connected, &connections, timestamp{&m.CreatedAt}, timestamp{&m.UpdatedAt}); err != nil {
			return nil, err
		}
		m.Unread = unread.Valid
		if unread.Valid {
			var t time.Time
			if err := (timestamp{&t}).parse(unread.String); err != nil {
				return nil, err
			}
			m.UnreadAt = &t
		}
		memberships = append(memberships, m)
	}
	return memberships, rows.Err()
}

// AccountSettingsRestrictRoomCreation is `settings.restrict_room_creation_to_administrators?`:
// present? of the stored value (has_json's default is false).
func AccountSettingsRestrictRoomCreation(settings []byte) bool {
	var data map[string]any
	if json.Unmarshal(settings, &data) != nil {
		return false
	}
	switch v := data["restrict_room_creation_to_administrators"].(type) {
	case bool:
		return v
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	case float64:
		return true
	}
	return false
}

// castBoolean is ActiveModel::Type::Boolean#cast of a form value: blank is nil, the FALSE_VALUES
// are false, anything else is true.
func castBoolean(value string) any {
	if value == "" {
		return nil
	}
	switch value {
	case "0", "f", "F", "false", "FALSE", "off", "OFF":
		return false
	}
	return true
}

// ErrUnknownSetting is assigning a setting the account's has_json schema doesn't have.
var ErrUnknownSetting = errorString("undefined method for account settings")

type errorString string

func (e errorString) Error() string { return string(e) }

// AccountUpdate is `account.update!(name:, custom_styles:, settings:)`, then the logo
// assignment: only changed attributes are written, and nothing (not even updated_at) when none
// changed. customStyles is nil to leave them, settings nil to leave them.
func (d *DB) AccountUpdate(ctx context.Context, account Account, name *string, customStyles *NullString, settings [][2]string, uploads ...BlobStager) error {
	id := account.ID
	return d.recordWithUpload(ctx, "Account", &id, d.touching(uploads, "accounts", "Account", "logo", &id), func(tx *Tx) error {
		var columns []string
		var args []any
		if name != nil && *name != account.Name {
			columns, args = append(columns, "name"), append(args, *name)
		}
		if customStyles != nil && (customStyles.Valid != account.HasCustomStyles || customStyles.String != account.CustomStyles) {
			var value any
			if customStyles.Valid {
				value = customStyles.String
			}
			columns, args = append(columns, "custom_styles"), append(args, value)
		}
		if settings != nil {
			// The settings column as stored: a NULL one is written even when nothing changed.
			var raw NullString
			if err := tx.QueryRowContext(ctx, `SELECT "accounts"."settings" FROM "accounts" WHERE "accounts"."id" = ? LIMIT 1`, id).Scan(&raw); err != nil {
				return err
			}
			original := map[string]any{}
			stored := raw.Valid
			if stored {
				json.Unmarshal([]byte(raw.String), &original)
			}
			if original == nil {
				original = map[string]any{}
			}
			if _, ok := original["restrict_room_creation_to_administrators"]; !ok {
				original["restrict_room_creation_to_administrators"] = false
			}
			updated := make(map[string]any, len(original))
			for key, value := range original {
				updated[key] = value
			}
			for _, setting := range settings {
				if setting[0] != "restrict_room_creation_to_administrators" {
					return ErrUnknownSetting
				}
				updated[setting[0]] = castBoolean(setting[1])
			}
			before, _ := json.Marshal(original)
			after, err := json.Marshal(updated)
			if err != nil {
				return err
			}
			if !stored || string(before) != string(after) {
				columns, args = append(columns, "settings"), append(args, string(after))
			}
		}
		if len(columns) == 0 {
			return nil
		}
		columns, args = append(columns, "updated_at"), append(args, Stamp(d.Now()))
		assignments := make([]string, len(columns))
		for i, column := range columns {
			assignments[i] = `"` + column + `" = ?`
		}
		_, err := tx.ExecContext(ctx, `UPDATE "accounts" SET `+strings.Join(assignments, ", ")+` WHERE "accounts"."id" = ?`, append(args, id)...)
		return err
	})
}

// touching makes an attachment assignment touch its record, as attaching a blob or destroying an
// existing attachment does (`belongs_to :record, touch: true`).
func (d *DB) touching(uploads []BlobStager, table, recordType, name string, id *int64) []BlobStager {
	if len(uploads) == 0 || uploads[0] == nil {
		return uploads
	}
	return []BlobStager{touchingStager{uploads[0], d, table, recordType, name, id}}
}

type touchingStager struct {
	BlobStager
	d                       *DB
	table, recordType, name string
	id                      *int64
}

func (s touchingStager) Insert(ctx context.Context, tx *Tx) (int64, error) {
	blob, err := s.BlobStager.Insert(ctx, tx)
	if err != nil {
		return blob, err
	}
	if blob == 0 {
		var attachment int64
		err := tx.QueryRowContext(ctx, "SELECT id, blob_id FROM active_storage_attachments WHERE record_type = ?1 AND record_id = ?2 AND name = ?3 LIMIT 1", s.recordType, *s.id, s.name).Scan(&attachment, new(int64))
		if err == ErrNoRows {
			return blob, nil
		}
		if err != nil {
			return blob, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE "`+s.table+`" SET "updated_at" = ? WHERE "`+s.table+`"."id" = ?`, Stamp(s.d.Now()), *s.id)
	return blob, err
}

// AccountResetJoinCode is Account#reset_join_code.
func (d *DB) AccountResetJoinCode(ctx context.Context, id int64) error {
	return d.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE "accounts" SET "join_code" = ?, "updated_at" = ? WHERE "accounts"."id" = ?`, generateJoinCode(), Stamp(d.Now()), id)
		return err
	})
}

// generateJoinCode is `SecureRandom.alphanumeric(12).scan(/.{4}/).join("-")`.
func generateJoinCode() string {
	code := alphanumeric(12)
	return code[0:4] + "-" + code[4:8] + "-" + code[8:12]
}

// alphanumeric is SecureRandom.alphanumeric(n).
func alphanumeric(n int) string { return RandomToken(n) }

// userUpdate is User#update for the given changed columns: only values that differ are
// written, with updated_at, and nothing at all when none do.
func userUpdate(ctx context.Context, tx *Tx, now time.Time, id int64, columns []string, args []any) error {
	if len(columns) == 0 {
		return nil
	}
	columns, args = append(columns, "updated_at"), append(args, Stamp(now))
	assignments := make([]string, len(columns))
	for i, column := range columns {
		assignments[i] = `"` + column + `" = ?`
	}
	_, err := tx.ExecContext(ctx, `UPDATE "users" SET `+strings.Join(assignments, ", ")+` WHERE "users"."id" = ?`, append(args, id)...)
	return err
}

// UserUpdateRole is `user.update(role:)`.
func (d *DB) UserUpdateRole(ctx context.Context, user User, role int) error {
	if role == user.Role {
		return nil
	}
	return d.Transaction(ctx, func(tx *Tx) error {
		return userUpdate(ctx, tx, d.Now(), user.ID, []string{"role"}, []any{role})
	})
}

// UserResetBotKey is User#reset_bot_key.
func (d *DB) UserResetBotKey(ctx context.Context, user User) error {
	return d.Transaction(ctx, func(tx *Tx) error {
		token := alphanumeric(12)
		if token == user.BotToken {
			return nil
		}
		return userUpdate(ctx, tx, d.Now(), user.ID, []string{"bot_token"}, []any{token})
	})
}

// UserCreateBot is `User.create_bot!(name:, webhook_url:)`, then the avatar assignment: the bot,
// its webhook when a URL (even "") was given, and after commit a membership of every open room.
func (d *DB) UserCreateBot(ctx context.Context, name string, webhookURL *string, uploads ...BlobStager) error {
	var id int64
	return d.recordWithUpload(ctx, "User", &id, d.touching(uploads, "users", "User", "avatar", &id), func(tx *Tx) error {
		now := Stamp(d.Now())
		err := tx.QueryRowContext(ctx, `INSERT INTO "users" ("bio", "bot_token", "created_at", "email_address", "name", "password_digest", "role", "status", "updated_at") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING "id"`,
			nil, alphanumeric(12), now, nil, name, nil, 2, 0, now).Scan(&id)
		if err != nil {
			return err
		}
		userID := id
		tx.AfterCommit(func(tx *Tx) error { return grantMembershipToOpenRooms(ctx, tx, userID) })
		if webhookURL != nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO "webhooks" ("created_at", "updated_at", "url", "user_id") VALUES (?, ?, ?, ?) RETURNING "id"`, now, now, *webhookURL, id)
		}
		return err
	})
}

// grantMembershipToOpenRooms is User#grant_membership_to_open_rooms.
func grantMembershipToOpenRooms(ctx context.Context, tx *Tx, user int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT "rooms"."id" FROM "rooms" WHERE "rooms"."type" = ?`, "Rooms::Open")
	if err != nil {
		return err
	}
	rooms, err := scanIDs(rows)
	if err != nil {
		return err
	}
	for batch := range slices.Chunk(rooms, membershipInsertBatch) {
		values := make([]string, len(batch))
		args := make([]any, 0, 2*len(batch))
		for i, room := range batch {
			values[i] = "(" + sqliteNow + ", ?, " + sqliteNow + ", ?)"
			args = append(args, room, user)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO "memberships" ("created_at","room_id","updated_at","user_id") VALUES `+strings.Join(values, ", ")+` ON CONFLICT  DO NOTHING RETURNING "id"`, args...); err != nil {
			return err
		}
	}
	return nil
}

// UserUpdateBot is `bot.update_bot!(name:, webhook_url:)` and the avatar assignment: the webhook
// first (a blank URL removes it), then the bot's name if it changed.
func (d *DB) UserUpdateBot(ctx context.Context, bot User, name *string, webhookURL *string, uploads ...BlobStager) error {
	id := bot.ID
	return d.recordWithUpload(ctx, "User", &id, d.touching(uploads, "users", "User", "avatar", &id), func(tx *Tx) error {
		var webhookID int64
		var current NullString
		var created, updated string
		var userID int64
		err := tx.QueryRowContext(ctx, `SELECT "webhooks".* FROM "webhooks" WHERE "webhooks"."user_id" = ? LIMIT 1`, id).Scan(&webhookID, &created, &updated, &current, &userID)
		if err != nil && err != ErrNoRows {
			return err
		}
		found := err == nil
		now := Stamp(d.Now())
		switch url := webhookURL; {
		case url != nil && strings.TrimSpace(*url) != "" && found:
			if !current.Valid || current.String != *url {
				_, err = tx.ExecContext(ctx, `UPDATE "webhooks" SET "updated_at" = ?, "url" = ? WHERE "webhooks"."id" = ?`, now, *url, webhookID)
			} else {
				err = nil
			}
		case url != nil && strings.TrimSpace(*url) != "":
			_, err = tx.ExecContext(ctx, `INSERT INTO "webhooks" ("created_at", "updated_at", "url", "user_id") VALUES (?, ?, ?, ?) RETURNING "id"`, now, now, *url, id)
		case found:
			_, err = tx.ExecContext(ctx, `DELETE FROM "webhooks" WHERE "webhooks"."id" = ?`, webhookID)
		default:
			err = nil
		}
		if err != nil {
			return err
		}
		if name != nil && *name != bot.Name {
			return userUpdate(ctx, tx, d.Now(), id, []string{"name"}, []any{*name})
		}
		return nil
	})
}

// membershipInsertBatch is MEMBERSHIP_INSERT_BATCH: rows per INSERT of memberships.
const membershipInsertBatch = 1000

// insertMemberships is Room#grant_to's Membership.insert_all.
func insertMemberships(ctx context.Context, tx *Tx, room int64, involvement string, users []int64) error {
	for batch := range slices.Chunk(users, membershipInsertBatch) {
		values := make([]string, len(batch))
		args := make([]any, 0, 3*len(batch))
		for i, user := range batch {
			values[i] = "(" + sqliteNow + ", ?, ?, " + sqliteNow + ", ?)"
			args = append(args, involvement, room, user)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO "memberships" ("created_at","involvement","room_id","updated_at","user_id") VALUES `+strings.Join(values, ", ")+` ON CONFLICT  DO NOTHING RETURNING "id"`, args...); err != nil {
			return err
		}
	}
	return nil
}

func defaultInvolvement(roomType string) string {
	if roomType == "Rooms::Direct" {
		return "everything"
	}
	return "mentions"
}

// grantToActiveUsers is Rooms::Open's after_save_commit: `memberships.grant_to(User.active)`.
func grantToActiveUsers(ctx context.Context, tx *Tx, room int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT "users"."id" FROM "users" WHERE "users"."status" = ?`, 0)
	if err != nil {
		return err
	}
	users, err := scanIDs(rows)
	if err != nil {
		return err
	}
	found, err := scanReferenceRoom(tx.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM "rooms" WHERE "rooms"."id" = ? LIMIT 1`, room))
	if err != nil {
		return err
	}
	return insertMemberships(ctx, tx, room, defaultInvolvement(found.Type), users)
}

// existingUserIDs is `User.where(id: ids)`'s ids.
func existingUserIDs(ctx context.Context, tx *Tx, ids []int64) ([]int64, error) {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := tx.QueryContext(ctx, selectUsers+` WHERE "users"."id" IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	users, err := scanReferenceUsers(rows)
	if err != nil {
		return nil, err
	}
	existing := make([]int64, len(users))
	for i, u := range users {
		existing[i] = u.ID
	}
	return existing, nil
}

// createRoomFor is Room.create_for(name:, creator:, users:): the room, its granted memberships,
// and for an open room every active user after commit.
func (d *DB) createRoomFor(ctx context.Context, tx *Tx, roomType string, name *string, creator int64, users []int64) (ReferenceRoom, error) {
	now := Stamp(d.Now())
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO "rooms" ("created_at", "creator_id", "name", "type", "updated_at") VALUES (?, ?, ?, ?, ?) RETURNING "id"`, now, creator, name, roomType, now).Scan(&id); err != nil {
		return ReferenceRoom{}, err
	}
	if roomType == "Rooms::Open" {
		tx.AfterCommit(func(tx *Tx) error { return grantToActiveUsers(ctx, tx, id) })
	}
	room, err := scanReferenceRoom(tx.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM "rooms" WHERE "rooms"."id" = ? LIMIT 1`, id))
	if err != nil {
		return room, err
	}
	return room, insertMemberships(ctx, tx, id, defaultInvolvement(roomType), users)
}

// RoomCreateFor is `Rooms::<Type>.create_for(room_params, users:)`; with existing, the users are
// those of userIDs that exist (`User.where(id:)`).
func (d *DB) RoomCreateFor(ctx context.Context, roomType string, name *string, creator int64, userIDs []int64, existing bool) (ReferenceRoom, error) {
	var room ReferenceRoom
	err := d.Transaction(ctx, func(tx *Tx) error {
		users := userIDs
		if existing {
			var err error
			if users, err = existingUserIDs(ctx, tx, userIDs); err != nil {
				return err
			}
		}
		var err error
		room, err = d.createRoomFor(ctx, tx, roomType, name, creator, users)
		return err
	})
	return room, err
}

// RoomFindOrCreateDirectFor is `Rooms::Direct.find_or_create_for(User.where(id: userIDs))`: the
// direct room whose members are exactly those users, created by creator if there isn't one.
func (d *DB) RoomFindOrCreateDirectFor(ctx context.Context, userIDs []int64, creator int64) (ReferenceRoom, error) {
	var room ReferenceRoom
	err := d.Transaction(ctx, func(tx *Tx) error {
		users, err := existingUserIDs(ctx, tx, userIDs)
		if err != nil {
			return err
		}
		wanted := uniqueIDs(users)
		rows, err := tx.QueryContext(ctx, `SELECT `+roomColumns+` FROM "rooms" INNER JOIN "memberships" ON "memberships"."room_id" = "rooms"."id" INNER JOIN "users" ON "users"."id" = "memberships"."user_id" WHERE "rooms"."type" = ?`, "Rooms::Direct")
		if err != nil {
			return err
		}
		var candidates []ReferenceRoom
		for rows.Next() {
			candidate, err := scanReferenceRoom(rows)
			if err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, candidate)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, candidate := range candidates {
			rows, err := tx.QueryContext(ctx, roomUserIDs, candidate.ID)
			if err != nil {
				return err
			}
			members, err := scanIDs(rows)
			if err != nil {
				return err
			}
			if slices.Equal(uniqueIDs(members), wanted) {
				room = candidate
				return nil
			}
		}
		room, err = d.createRoomFor(ctx, tx, "Rooms::Direct", nil, creator, users)
		return err
	})
	return room, err
}

// RoomUpdate is `room.update!(name:, type:)` with the forced type: name nil leaves it. Becoming
// open grants every active user after commit.
func (d *DB) RoomUpdate(ctx context.Context, room ReferenceRoom, name *NullString, roomType string) (ReferenceRoom, error) {
	nameChanged := name != nil && *name != room.Name
	typeChanged := roomType != room.Type
	if typeChanged && room.Type == "Rooms::Direct" {
		return room, ErrValidation
	}
	if !nameChanged && !typeChanged {
		return room, nil
	}
	if nameChanged {
		room.Name = *name
	}
	room.Type = roomType
	room.UpdatedAt = d.Now()
	err := d.Transaction(ctx, func(tx *Tx) error {
		var value any
		if room.Name.Valid {
			value = room.Name.String
		}
		if _, err := tx.ExecContext(ctx, `UPDATE "rooms" SET "name" = ?, "type" = ?, "updated_at" = ? WHERE "rooms"."id" = ?`, value, room.Type, Stamp(room.UpdatedAt), room.ID); err != nil {
			return err
		}
		if typeChanged && roomType == "Rooms::Open" {
			id := room.ID
			tx.AfterCommit(func(tx *Tx) error { return grantToActiveUsers(ctx, tx, id) })
		}
		return nil
	})
	return room, err
}

// RoomRevise is Rooms::ClosedsController#update's `@room.memberships.revise(granted: grantees,
// revoked: revokees)`: grantees are the users of granteeIDs that exist, revokees the members not
// among granteeIDs. Each revoked member's connections are reset after commit.
func (d *DB) RoomRevise(ctx context.Context, room ReferenceRoom, granteeIDs []int64) error {
	var revoked []int64
	err := d.Transaction(ctx, func(tx *Tx) error {
		granted, err := existingUserIDs(ctx, tx, granteeIDs)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, roomUserIDs, room.ID)
		if err != nil {
			return err
		}
		members, err := scanIDs(rows)
		if err != nil {
			return err
		}
		var revokees []int64
		for _, member := range members {
			if !slices.Contains(granteeIDs, member) {
				revokees = append(revokees, member)
			}
		}
		if len(granted) > 0 {
			if err := insertMemberships(ctx, tx, room.ID, defaultInvolvement(room.Type), granted); err != nil {
				return err
			}
		}
		if len(revokees) == 0 {
			return nil
		}
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(revokees)), ", ")
		args := []any{room.ID}
		for _, id := range revokees {
			args = append(args, id)
		}
		rows, err = tx.QueryContext(ctx, `SELECT "memberships"."id", "memberships"."user_id" FROM "memberships" WHERE "memberships"."room_id" = ? AND "memberships"."user_id" IN (`+marks+`)`, args...)
		if err != nil {
			return err
		}
		type membership struct{ id, user int64 }
		var memberships []membership
		for rows.Next() {
			var m membership
			if err := rows.Scan(&m.id, &m.user); err != nil {
				rows.Close()
				return err
			}
			memberships = append(memberships, m)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, m := range memberships {
			if _, err := tx.ExecContext(ctx, `DELETE FROM "memberships" WHERE "memberships"."id" = ?`, m.id); err != nil {
				return err
			}
			revoked = append(revoked, m.user)
		}
		return nil
	})
	if err == nil && d.ResetConnections != nil {
		for _, user := range revoked {
			d.ResetConnections(user)
		}
	}
	return err
}
