package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Account struct {
	ID                           int64
	Name, JoinCode, CustomStyles string
	Settings                     json.RawMessage
	UpdatedAt                    time.Time
	HasLogo                      bool
}

func (d *DB) Account(ctx context.Context) (Account, error) {
	var a Account
	var settings string
	err := d.Read.QueryRowContext(ctx, "SELECT id,name,join_code,coalesce(custom_styles,''),coalesce(settings,'{}'),updated_at,EXISTS(SELECT 1 FROM active_storage_attachments WHERE record_type='Account' AND record_id=accounts.id AND name='logo') FROM accounts ORDER BY id LIMIT 1").
		Scan(&a.ID, &a.Name, &a.JoinCode, &a.CustomStyles, &settings, timestamp{&a.UpdatedAt}, &a.HasLogo)
	a.Settings = json.RawMessage(settings)
	return a, err
}

func (a Account) RestrictRooms() bool {
	var s struct {
		Restrict bool `json:"restrict_room_creation_to_administrators"`
	}
	json.Unmarshal(a.Settings, &s)
	return s.Restrict
}

func RandomToken(length int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 0, length)
	var b [64]byte
	for len(out) < length {
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		for _, v := range b {
			if v < 248 {
				out = append(out, alphabet[int(v)%len(alphabet)])
				if len(out) == length {
					break
				}
			}
		}
	}
	return string(out)
}

func (d *DB) User(ctx context.Context, id int64) (User, error) {
	return userRow(
		d.Read.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users u WHERE u.id=?", id),
	)
}

// UserContact is the complete login-help projection; no credentials are read.
type UserContact struct {
	ID          int64
	Name, Email string
}

func (d *DB) LoginHelpContact(ctx context.Context) (UserContact, error) {
	var contact UserContact
	err := d.Read.QueryRowContext(ctx, "SELECT id,name,coalesce(email_address,'') FROM users WHERE status=0 AND role=1 ORDER BY id LIMIT 1").Scan(&contact.ID, &contact.Name, &contact.Email)
	return contact, err
}

func (d *DB) Bot(ctx context.Context, key string) (User, error) {
	id, token, ok := strings.Cut(strings.TrimSpace(key), "-")
	if !ok {
		return User{}, sql.ErrNoRows
	}
	return userRow(
		d.Read.QueryRowContext(
			ctx,
			"SELECT "+userColumns+" FROM users u WHERE u.id=? AND u.bot_token=? AND u.role=2 AND u.status=0",
			id,
			token,
		),
	)
}

func (d *DB) BannedIP(ctx context.Context, ip string) (bool, error) {
	var n int
	err := d.Read.QueryRowContext(ctx, "SELECT count(*) FROM bans WHERE ip_address=?", ip).Scan(&n)
	return n > 0, err
}

func (u User) BotKey() string { return fmt.Sprintf("%d-%s", u.ID, u.BotToken) }

// AccountMember includes display state and the role/status used by management
// controls, but never credentials or private contact information.
type AccountMember struct {
	UserDisplay
	Role, Status int
}

func (d *DB) AccountUsers(ctx context.Context, includeBanned bool) ([]AccountMember, error) {
	status := "u.status=0"
	if includeBanned {
		status = "u.status IN (0,2)"
	}
	rows, err := d.Read.QueryContext(
		ctx,
		"SELECT u.id,u.name,coalesce(u.bio,''),u.updated_at,u.role,u.status FROM users u WHERE "+status+" AND u.role != 2 ORDER BY lower(u.name)",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []AccountMember
	for rows.Next() {
		var user AccountMember
		if err := rows.Scan(&user.ID, &user.Name, &user.Bio, timestamp{&user.UpdatedAt}, &user.Role, &user.Status); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (d *DB) RoomParticipants(ctx context.Context, room int64) ([]RoomParticipant, error) {
	rows, err := d.Read.QueryContext(
		ctx,
		"SELECT u.id,u.name,u.updated_at FROM users u JOIN memberships m ON m.user_id=u.id WHERE m.room_id=? ORDER BY m.user_id",
		room,
	)
	if err != nil {
		return nil, err
	}
	return participantsRows(rows)
}

func (d *DB) DirectPlaceholders(ctx context.Context, user int64) ([]RoomParticipant, error) {
	// Exclusions include invisible directs and inactive participants. Appending
	// the viewer consumes one slot even when they are already in the distinct set.
	// At nineteen distinct exclusions no placeholders remain, so further
	// exclusions cannot affect the result.
	rows, err := d.Read.QueryContext(ctx, `
WITH excluded AS MATERIALIZED (
 SELECT DISTINCT user_id FROM memberships WHERE room_id IN (
  SELECT r.id FROM rooms r JOIN memberships m ON m.room_id=r.id
  WHERE r.type='Rooms::Direct' AND m.user_id=?
 ) LIMIT 19
)
SELECT u.id,u.name,u.updated_at FROM users u
WHERE u.status=0 AND u.id!=? AND u.id NOT IN (SELECT user_id FROM excluded)
ORDER BY u.created_at ASC LIMIT max(0,19-(SELECT count(*) FROM excluded))`, user, user)
	if err != nil {
		return nil, err
	}
	return participantsRows(rows)
}

func participantsRows(rows *sql.Rows) ([]RoomParticipant, error) {
	defer rows.Close()
	var participants []RoomParticipant
	for rows.Next() {
		var participant RoomParticipant
		if err := rows.Scan(&participant.ID, &participant.Name, timestamp{&participant.UpdatedAt}); err != nil {
			return nil, err
		}
		participants = append(participants, participant)
	}
	return participants, rows.Err()
}
