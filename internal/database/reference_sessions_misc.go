package database

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"crawshaw.io/sqlite"
)

// The reference's queries for sign in, session transfers, the root URL and avatars
// (reference/crates/db/src/models, reference/crates/storage, and the presenters of
// reference/crates/campfire/src/controllers), with the reference's SQL text.

// UserCount is User::count.
func (d *DB) UserCount(ctx context.Context) (int64, error) {
	var n int64
	err := d.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM "users"`).Scan(&n)
	return n, err
}

// AccountCount is Account::count.
func (d *DB) AccountCount(ctx context.Context) (int64, error) {
	var n int64
	err := d.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM "accounts"`).Scan(&n)
	return n, err
}

// HelpContact is presenters::accounts::help_contact: User.administrator.first's name and email
// address.
func (d *DB) HelpContact(ctx context.Context) (name, emailAddress string, found bool, err error) {
	var email NullString
	err = d.Read.QueryRowContext(ctx, `SELECT "users"."name", "users"."email_address" FROM "users" WHERE "users"."role" = 1 ORDER BY "users"."id" ASC LIMIT 1`).Scan(&name, &email)
	if err == ErrNoRows {
		return "", "", false, nil
	}
	return name, email.String, err == nil, err
}

// UserFindActiveByEmailAddress is User::find_active_by_email_address.
func (d *DB) UserFindActiveByEmailAddress(ctx context.Context, emailAddress string) (User, bool, error) {
	u, err := ScanReferenceUser(d.Read.QueryRowContext(ctx, `SELECT `+UserColumns+` FROM "users" WHERE "users"."status" = 0 AND "users"."email_address" = ? LIMIT 1`, emailAddress))
	if err == ErrNoRows {
		return User{}, false, nil
	}
	return u, err == nil, err
}

// SessionStart is Session::start: a new session with a has_secure_token token.
func (d *DB) SessionStart(ctx context.Context, user int64, userAgent, ip *string) (string, error) {
	token := base58(24)
	err := d.Transaction(ctx, func(tx *Tx) error {
		now := Stamp(d.Now())
		var id int64
		return tx.QueryRowContext(ctx, `INSERT INTO "sessions" ("created_at", "ip_address", "last_active_at", "token", "updated_at", "user_agent", "user_id") VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING "id"`, now, ip, now, token, now, userAgent, user).Scan(&id)
	})
	return token, err
}

// SessionDestroy is Session#destroy.
func (d *DB) SessionDestroy(ctx context.Context, id int64) error {
	return d.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM "sessions" WHERE "sessions"."id" = ?`, id)
		return err
	})
}

// PushSubscriptionDestroyByEndpoint is PushSubscription::destroy_by_endpoint:
// `Push::Subscription.destroy_by(endpoint:, user_id:)`.
func (d *DB) PushSubscriptionDestroyByEndpoint(ctx context.Context, user int64, endpoint string) error {
	return d.Transaction(ctx, func(tx *Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT "push_subscriptions"."id" FROM "push_subscriptions" WHERE "push_subscriptions"."endpoint" = ? AND "push_subscriptions"."user_id" = ?`, endpoint, user)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `DELETE FROM "push_subscriptions" WHERE "push_subscriptions"."id" = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// UserHasRooms is `!Room::for_user(user).is_empty()`: Current.user.rooms.any?.
func (d *DB) UserHasRooms(ctx context.Context, user int64) (bool, error) {
	rows, err := d.Read.QueryContext(ctx, selectForUser, user)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

// LastRoomForUser is Room::last_for_user: the user's newest room.
func (d *DB) LastRoomForUser(ctx context.Context, user int64) (ReferenceRoom, bool, error) {
	return d.optionalRoom(ctx, selectForUser+` ORDER BY "rooms"."id" DESC LIMIT 1`, user)
}

// ExistingVariant is Storage::existing_variant: the image of the blob's variant recorded under
// digest, if it has been processed.
func (d *DB) ExistingVariant(ctx context.Context, blob int64, digest string) (Blob, bool, error) {
	var record int64
	err := d.Read.QueryRowContext(ctx, "SELECT id FROM active_storage_variant_records WHERE blob_id = ?1 AND variation_digest = ?2", blob, digest).Scan(&record)
	if err == ErrNoRows {
		return Blob{}, false, nil
	}
	if err != nil {
		return Blob{}, false, err
	}
	return d.AttachedBlob(ctx, "ActiveStorage::VariantRecord", record, "image")
}

// FirstRunCreate is FirstRun::create: the account, its first administrator (with avatar, an
// upload or attachment assignment, attached in the same transaction) and the first open room.
// After commit, as the reference's callbacks run: the administrator joins the open rooms, the
// room is granted to every active user, then to the administrator.
func (d *DB) FirstRunCreate(ctx context.Context, name, emailAddress, passwordDigest string, avatar BlobStager) (User, error) {
	var u User
	var uploads []BlobStager
	if avatar != nil {
		uploads = append(uploads, avatar)
	}
	err := d.recordWithUpload(ctx, "User", &u.ID, uploads, func(tx *Tx) error {
		now := Stamp(d.Now())
		var account int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO "accounts" ("created_at", "custom_styles", "join_code", "name", "settings", "singleton_guard", "updated_at") VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING "id"`,
			now, nil, generateJoinCode(), "Campfire", `{"restrict_room_creation_to_administrators":false}`, 0, now).Scan(&account); err != nil {
			return err
		}
		var id, guard int64
		var accountName, joinCode string
		var customStyles, settings NullString
		var created, updated time.Time
		if err := tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM "accounts" WHERE "accounts"."id" = ? LIMIT 1`, account).
			Scan(&id, &accountName, &joinCode, &customStyles, &settings, &guard, timestamp{&created}, timestamp{&updated}); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO "users" ("bio", "bot_token", "created_at", "email_address", "name", "password_digest", "role", "status", "updated_at") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING "id"`,
			nil, nil, now, emailAddress, name, passwordDigest, 1, 0, now).Scan(&u.ID); err != nil {
			return err
		}
		administrator := u.ID
		tx.AfterCommit(func(tx *Tx) error { return grantMembershipToOpenRooms(ctx, tx, administrator) })
		var err error
		if u, err = ScanReferenceUser(tx.QueryRowContext(ctx, `SELECT `+UserColumns+` FROM "users" WHERE "users"."id" = ? LIMIT 1`, administrator)); err != nil {
			return err
		}
		var room int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO "rooms" ("created_at", "creator_id", "name", "type", "updated_at") VALUES (?, ?, ?, ?, ?) RETURNING "id"`,
			now, administrator, "All Talk", "Rooms::Open", now).Scan(&room); err != nil {
			return err
		}
		tx.AfterCommit(func(tx *Tx) error { return grantRoomToActiveUsers(ctx, tx, room) })
		if _, err := scanReferenceRoom(tx.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM "rooms" WHERE "rooms"."id" = ? LIMIT 1`, room)); err != nil {
			return err
		}
		tx.AfterCommit(func(tx *Tx) error { return insertMemberships(ctx, tx, room, "mentions", []int64{administrator}) })
		return nil
	})
	return u, err
}

// IsRecordNotUnique is whether err is a unique constraint violation (ActiveRecord::RecordNotUnique).
func IsRecordNotUnique(err error) bool {
	var sqliteErr sqlite.Error
	return errors.As(err, &sqliteErr) && (sqliteErr.Code == sqlite.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code == sqlite.SQLITE_CONSTRAINT_PRIMARYKEY)
}

// grantMembershipToOpenRooms is User's after_create_commit: memberships in every open room.
func grantMembershipToOpenRooms(ctx context.Context, tx *Tx, user int64) error {
	rooms, err := queryIDs(ctx, tx, `SELECT "rooms"."id" FROM "rooms" WHERE "rooms"."type" = ?`, "Rooms::Open")
	if err != nil {
		return err
	}
	for len(rooms) > 0 {
		batch := rooms[:min(len(rooms), membershipInsertBatch)]
		rooms = rooms[len(batch):]
		values := make([]any, 0, 2*len(batch))
		rows := make([]string, len(batch))
		for i, room := range batch {
			rows[i] = "(" + sqliteNow + ", ?, " + sqliteNow + ", ?)"
			values = append(values, room, user)
		}
		if err := drain(tx.QueryContext(ctx, `INSERT INTO "memberships" ("created_at","room_id","updated_at","user_id") VALUES `+strings.Join(rows, ", ")+` ON CONFLICT  DO NOTHING RETURNING "id"`, values...)); err != nil {
			return err
		}
	}
	return nil
}

// grantRoomToActiveUsers is Rooms::Open's after_save_commit: `memberships.grant_to(User.active)`.
func grantRoomToActiveUsers(ctx context.Context, tx *Tx, room int64) error {
	users, err := queryIDs(ctx, tx, `SELECT "users"."id" FROM "users" WHERE "users"."status" = ?`, 0)
	if err != nil {
		return err
	}
	if _, err := scanReferenceRoom(tx.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM "rooms" WHERE "rooms"."id" = ? LIMIT 1`, room)); err != nil {
		return err
	}
	return insertMemberships(ctx, tx, room, "mentions", users)
}

// insertMemberships is `Membership.insert_all(... { room_id:, user_id:, involvement: })`, in
// batches.
func insertMemberships(ctx context.Context, tx *Tx, room int64, involvement string, users []int64) error {
	for len(users) > 0 {
		batch := users[:min(len(users), membershipInsertBatch)]
		users = users[len(batch):]
		values := make([]any, 0, 3*len(batch))
		rows := make([]string, len(batch))
		for i, user := range batch {
			rows[i] = "(" + sqliteNow + ", ?, ?, " + sqliteNow + ", ?)"
			values = append(values, involvement, room, user)
		}
		if err := drain(tx.QueryContext(ctx, `INSERT INTO "memberships" ("created_at","involvement","room_id","updated_at","user_id") VALUES `+strings.Join(rows, ", ")+` ON CONFLICT  DO NOTHING RETURNING "id"`, values...)); err != nil {
			return err
		}
	}
	return nil
}

const (
	// membershipInsertBatch is the reference's MEMBERSHIP_INSERT_BATCH.
	membershipInsertBatch = 1000
	// sqliteNow is the reference's SQLITE_NOW.
	sqliteNow = "STRFTIME('%Y-%m-%d %H:%M:%f', 'NOW')"
)

func queryIDs(ctx context.Context, tx *Tx, query string, args ...any) ([]int64, error) {
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

func drain(rows *Rows, err error) error {
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}

// generateJoinCode is Account's join code: twelve alphanumerics in three dashed groups.
func generateJoinCode() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	code := randomFrom(alphabet, 12)
	return code[0:4] + "-" + code[4:8] + "-" + code[8:12]
}

// base58 is SecureRandom.base58(n), has_secure_token's alphabet.
func base58(n int) string {
	return randomFrom("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz", n)
}

// randomFrom is n characters picked uniformly from alphabet.
func randomFrom(alphabet string, n int) string {
	limit := byte(256 - 256%len(alphabet))
	b := make([]byte, n)
	var r [1]byte
	for i := range b {
		for {
			rand.Read(r[:])
			if r[0] < limit {
				break
			}
		}
		b[i] = alphabet[int(r[0])%len(alphabet)]
	}
	return string(b)
}
