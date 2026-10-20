package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrForbidden  = errors.New("forbidden")
	ErrValidation = errors.New("invalid attributes")
)

type User struct {
	ID                    int64
	Name, Email, Password string
	Bio, BotToken         string
	UpdatedAt             time.Time
	Role, Status          int
}

// RoomParticipant is the owned display data for a ping or sidebar placeholder.
// Credentials, profile text and authorization state remain in User.
type RoomParticipant struct {
	ID        int64
	Name      string
	UpdatedAt time.Time
}

func (u User) Participant() RoomParticipant {
	return RoomParticipant{ID: u.ID, Name: u.Name, UpdatedAt: u.UpdatedAt}
}

func (u User) Title() string {
	return (UserDisplay{Name: u.Name, Bio: u.Bio}).Title()
}

type Room struct {
	ID, CreatorID int64
	Name, Type    string
	UpdatedAt     time.Time
}
type Message struct {
	ID, RoomID, CreatorID   int64
	ClientID, Body, Creator string
	CreatedAt, UpdatedAt    time.Time
}

func Token() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

const userColumns = "u.id,u.name,coalesce(u.email_address,''),coalesce(u.password_digest,''),u.role,u.status,coalesce(u.bio,''),u.updated_at,coalesce(u.bot_token,'')"

func userRow(row *sql.Row) (User, error) {
	var u User
	err := row.Scan(
		&u.ID,
		&u.Name,
		&u.Email,
		&u.Password,
		&u.Role,
		&u.Status,
		&u.Bio,
		timestamp{&u.UpdatedAt},
		&u.BotToken,
	)
	return u, err
}

func (d *DB) UserByEmail(ctx context.Context, email string) (User, error) {
	return userRow(
		d.Read.QueryRowContext(
			ctx,
			"SELECT "+userColumns+" FROM users u WHERE u.email_address=? AND u.status=0",
			email,
		),
	)
}

func (d *DB) Rooms(ctx context.Context, user int64) ([]Room, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE m.user_id=? AND m.involvement!='invisible' ORDER BY lower(r.name)", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Room{}
	for rows.Next() {
		var r Room
		if err = rows.Scan(&r.ID, &r.CreatorID, &r.Name, &r.Type, timestamp{&r.UpdatedAt}); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (d *DB) Room(ctx context.Context, user, id int64) (Room, error) {
	var r Room
	err := d.Read.QueryRowContext(ctx, "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE m.user_id=? AND r.id=?", user, id).
		Scan(&r.ID, &r.CreatorID, &r.Name, &r.Type, timestamp{&r.UpdatedAt})
	return r, err
}

const messageSelect = "SELECT m.id,m.room_id,m.creator_id,m.client_message_id,coalesce(t.body,''),coalesce(u.name,''),m.created_at,m.updated_at FROM messages m LEFT JOIN users u ON u.id=m.creator_id LEFT JOIN action_text_rich_texts t ON t.record_type='Message' AND t.record_id=m.id AND t.name='body' "

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	result := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.RoomID, &m.CreatorID, &m.ClientID, &m.Body, &m.Creator, timestamp{&m.CreatedAt}, timestamp{&m.UpdatedAt}); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// AuthorizeSessions replaces a publication-owned token map's values with current
// user IDs from one snapshot. Unknown or revoked sessions remain zero; any failure
// clears all permissions. The caller must not access the map during this call.
// json_each keeps the SQL shape stable and avoids SQLite's placeholder limit.
func (d *DB) AuthorizeSessions(
	ctx context.Context,
	sessions map[string]int64,
	room int64,
) (err error) {
	tokens := make([]string, 0, len(sessions))
	for token := range sessions {
		sessions[token] = 0
		tokens = append(tokens, token)
	}
	defer func() {
		if err != nil {
			for token := range sessions {
				sessions[token] = 0
			}
		}
	}()
	raw, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	query := "SELECT s.token,s.user_id FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.status=0 AND s.token IN (SELECT value FROM json_each(?))"
	args := []any{string(raw)}
	if room != 0 {
		query += " AND EXISTS (SELECT 1 FROM memberships m WHERE m.user_id=s.user_id AND m.room_id=?)"
		args = append(args, room)
	}
	rows, err := d.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var token string
		var user int64
		if err = rows.Scan(&token, &user); err != nil {
			return err
		}
		sessions[token] = user
	}
	return rows.Err()
}
