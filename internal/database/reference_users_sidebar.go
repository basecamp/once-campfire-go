package database

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// The reference's queries behind the users screens (sidebar, profile, push subscriptions, user
// page, join page, autocompletable users): reference/crates/db/src/models/{membership,room,user,
// push_subscription}.rs and controllers/presenters/accounts.rs, with the reference's SQL text.

const (
	membershipColumns = `"memberships"."id", "memberships"."room_id", "memberships"."user_id", "memberships"."involvement", "memberships"."unread_at", "memberships"."connected_at", "memberships"."connections", "memberships"."created_at", "memberships"."updated_at"`
	// SelectUsers is User::SELECT.
	SelectUsers = `SELECT ` + UserColumns + ` FROM "users"`
)

// MembershipRoom is a membership with its room (the reference's with_room join).
type MembershipRoom struct {
	Membership ReferenceMembership
	Room       ReferenceRoom
}

func scanMembershipRoom(rows *Rows, m *MembershipRoom) error {
	var unreadAt, connectedAt NullString
	var connections int64
	var created time.Time
	err := rows.Scan(&m.Membership.ID, &m.Membership.RoomID, &m.Membership.UserID, &m.Membership.Involvement, &unreadAt, &connectedAt, &connections, timestamp{&created}, timestamp{&m.Membership.UpdatedAt},
		&m.Room.ID, &m.Room.Name, &m.Room.Type, &m.Room.CreatorID, timestamp{&m.Room.CreatedAt}, timestamp{&m.Room.UpdatedAt})
	m.Membership.Unread = unreadAt.Valid
	return err
}

func (d *DB) membershipRooms(ctx context.Context, query string, args ...any) ([]MembershipRoom, error) {
	rows, err := d.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Scanned in place, in a slice most users' rooms fit.
	list := make([]MembershipRoom, 0, 16)
	for rows.Next() {
		list = append(list, MembershipRoom{})
		if err := scanMembershipRoom(rows, &list[len(list)-1]); err != nil {
			return nil, err
		}
	}
	return list, rows.Err()
}

// MembershipsVisibleWithOrderedRoom is Membership::visible_with_ordered_room:
// `user.memberships.visible.with_ordered_room`.
func (d *DB) MembershipsVisibleWithOrderedRoom(ctx context.Context, user int64) ([]MembershipRoom, error) {
	return d.membershipRooms(ctx, `SELECT `+membershipColumns+`, `+roomColumns+` FROM "memberships" INNER JOIN "rooms" ON "rooms"."id" = "memberships"."room_id" WHERE "memberships"."user_id" = ? AND "memberships"."involvement" != 'invisible' ORDER BY LOWER(rooms.name)`, user)
}

// MembershipsWithOrderedRoom is Membership::with_ordered_room: `user.memberships.with_ordered_room`
// (invisible included).
func (d *DB) MembershipsWithOrderedRoom(ctx context.Context, user int64) ([]MembershipRoom, error) {
	return d.membershipRooms(ctx, `SELECT `+membershipColumns+`, `+roomColumns+` FROM "memberships" INNER JOIN "rooms" ON "rooms"."id" = "memberships"."room_id" WHERE "memberships"."user_id" = ? ORDER BY LOWER(rooms.name)`, user)
}

// RoomFindByID is Room::find_by_id.
func (d *DB) RoomFindByID(ctx context.Context, id int64) (ReferenceRoom, bool, error) {
	return d.optionalRoom(ctx, `SELECT `+roomColumns+` FROM "rooms" WHERE "rooms"."id" = ? LIMIT 1`, id)
}

// UsersBySQL is User::find_by_sql: the users a query starting with SelectUsers returns.
func (d *DB) UsersBySQL(ctx context.Context, query string, args ...any) ([]User, error) {
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

// RoomsForUserOfType is Room::for_user_of_type: `user.rooms.directs` / `.opens` / `.closeds`;
// roomType is the STI class ("Rooms::Direct").
func (d *DB) RoomsForUserOfType(ctx context.Context, user int64, roomType string) ([]ReferenceRoom, error) {
	rows, err := d.Read.QueryContext(ctx, selectForUser+` AND "rooms"."type" = ?`, user, roomType)
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

// placeholders is the reference's sql::placeholders: n "?" joined with ", ".
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func int64Args(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// MembershipUserIDsInRooms is `Membership.where(room_id: rooms).pluck(:user_id)`.
func (d *DB) MembershipUserIDsInRooms(ctx context.Context, rooms []int64) ([]int64, error) {
	rows, err := d.Read.QueryContext(ctx, `SELECT "memberships"."user_id" FROM "memberships" WHERE "memberships"."room_id" IN (`+placeholders(len(rooms))+`)`, int64Args(rooms)...)
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

// ActiveUsersNotIn is find_direct_placeholder_users' `User.active.where.not(id:).order(:created_at).limit(limit)`,
// with the limit in the SQL as the reference writes it.
func (d *DB) ActiveUsersNotIn(ctx context.Context, exclude []int64, limit int64) ([]User, error) {
	return d.UsersBySQL(ctx, SelectUsers+` WHERE "users"."status" = 0 AND "users"."id" NOT IN (`+placeholders(len(exclude))+`) ORDER BY "users"."created_at" ASC LIMIT `+strconv.FormatInt(limit, 10), int64Args(exclude)...)
}

// AutocompletableUsers is Autocompletable::UsersController's `users_scope.active[.filtered_by(query)].ordered`:
// room's users (all users when room is nil), whose name contains query when given.
func (d *DB) AutocompletableUsers(ctx context.Context, room *int64, query *string) ([]User, error) {
	var b strings.Builder
	b.WriteString(SelectUsers)
	var args []any
	if room != nil {
		b.WriteString(` INNER JOIN "memberships" ON "users"."id" = "memberships"."user_id" WHERE "memberships"."room_id" = ? AND`)
		args = append(args, *room)
	} else {
		b.WriteString(" WHERE")
	}
	b.WriteString(` "users"."status" = 0`)
	if query != nil {
		b.WriteString(" AND (name like ?)")
		args = append(args, "%"+*query+"%")
	}
	b.WriteString(" ORDER BY LOWER(name)")
	return d.UsersBySQL(ctx, b.String(), args...)
}

// ReferencePushSubscription is a push_subscriptions row as the reference's PushSubscription reads it.
type ReferencePushSubscription struct {
	ID, UserID                              int64
	Endpoint, P256dhKey, AuthKey, UserAgent NullString
	CreatedAt, UpdatedAt                    time.Time
}

// PushSubscriptionsForUser is PushSubscription::for_user: `user.push_subscriptions`.
func (d *DB) PushSubscriptionsForUser(ctx context.Context, user int64) ([]ReferencePushSubscription, error) {
	rows, err := d.Read.QueryContext(ctx, `SELECT * FROM "push_subscriptions" WHERE "push_subscriptions"."user_id" = ?`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ReferencePushSubscription
	for rows.Next() {
		var p ReferencePushSubscription
		// The schema's column order: id, auth_key, created_at, endpoint, p256dh_key, updated_at, user_agent, user_id.
		if err := rows.Scan(&p.ID, &p.AuthKey, timestamp{&p.CreatedAt}, &p.Endpoint, &p.P256dhKey, timestamp{&p.UpdatedAt}, &p.UserAgent, &p.UserID); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

// userInsert is User::create's INSERT.
const userInsert = `INSERT INTO "users" ("bio", "bot_token", "created_at", "email_address", "name", "password_digest", "role", "status", "updated_at") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING "id"`

// CreateMember is UsersController#create's `User.create!(user_params)`: an active member with no
// bio, granted the open rooms after commit, with its avatar attached when one is staged.
func (d *DB) CreateMember(ctx context.Context, name string, email, passwordDigest *string, uploads ...BlobStager) (User, error) {
	var u User
	err := d.recordWithUpload(ctx, "User", &u.ID, uploads, func(tx *Tx) error {
		now := Stamp(d.Now())
		if err := tx.QueryRowContext(ctx, userInsert, nil, nil, now, email, name, passwordDigest, 0, 0, now).Scan(&u.ID); err != nil {
			return err
		}
		id := u.ID
		tx.AfterCommit(func(tx *Tx) error { return grantMembershipToOpenRooms(ctx, tx, id) })
		created, err := ScanReferenceUser(tx.QueryRowContext(ctx, `SELECT `+UserColumns+` FROM "users" WHERE "users"."id" = ? LIMIT 1`, id))
		u = created
		return err
	})
	return u, err
}
