package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"uuid"
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

func (u User) Title() string {
	parts := []string{}
	for _, value := range []string{u.Name, u.Bio} {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " – ")
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

func (d *DB) SessionUser(ctx context.Context, token string) (User, error) {
	return userRow(
		d.Read.QueryRowContext(
			ctx,
			"SELECT "+userColumns+" FROM users u JOIN sessions s ON s.user_id=u.id WHERE s.token=? AND u.status=0",
			token,
		),
	)
}

func (d *DB) StartSession(ctx context.Context, user int64, agent, ip string) (string, error) {
	token, now := Token(), Stamp(d.Now())
	_, err := d.Write.ExecContext(
		ctx,
		"INSERT INTO sessions(token,user_id,user_agent,ip_address,last_active_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?)",
		token,
		user,
		agent,
		ip,
		now,
		now,
		now,
	)
	return token, err
}

func (d *DB) Setup(
	ctx context.Context,
	name, email, passwordDigest string,
	uploads ...BlobStager,
) (User, error) {
	var u User
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" || passwordDigest == "" {
		return u, ErrValidation
	}
	err := d.recordWithUpload(ctx, "User", &u.ID, uploads, func(tx *sql.Tx) error {
		now := Stamp(d.Now())
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return ErrForbidden
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(name,join_code,settings,created_at,updated_at) VALUES (?,?,?,?,?)", "Campfire", Token(), "{}", now, now); err != nil {
			return err
		}
		r, err := tx.ExecContext(
			ctx,
			"INSERT INTO users(name,email_address,password_digest,role,status,created_at,updated_at) VALUES (?,?,?,1,0,?,?)",
			name,
			email,
			passwordDigest,
			now,
			now,
		)
		if err != nil {
			return err
		}
		id, err := r.LastInsertId()
		if err != nil {
			return err
		}
		r, err = tx.ExecContext(
			ctx,
			"INSERT INTO rooms(name,type,creator_id,created_at,updated_at) VALUES (?,'Rooms::Open',?,?,?)",
			"All Talk",
			id,
			now,
			now,
		)
		if err != nil {
			return err
		}
		room, err := r.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(
			ctx,
			"INSERT INTO memberships(room_id,user_id,created_at,updated_at) VALUES (?,?,?,?)",
			room,
			id,
			now,
			now,
		)
		u = User{ID: id, Name: name, Email: email, Role: 1}
		return err
	})
	return u, err
}

func (d *DB) Rooms(
	ctx context.Context,
	user int64,
) ([]Room, error) {
	return d.rooms(ctx, user, true)
}

func (d *DB) AllRooms(ctx context.Context, user int64) ([]Room, error) {
	return d.rooms(ctx, user, false)
}

func (d *DB) rooms(ctx context.Context, user int64, visible bool) ([]Room, error) {
	query := "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE m.user_id=?"
	if visible {
		query += " AND m.involvement!='invisible'"
	}
	rows, err := d.Read.QueryContext(ctx, query+" ORDER BY lower(r.name)", user)
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

func (d *DB) Messages(ctx context.Context, room, before int64) ([]Message, error) {
	query := messageSelect + "WHERE m.room_id=? "
	args := []any{room}
	if before != 0 {
		query += "AND m.created_at < (SELECT created_at FROM messages WHERE id=? AND room_id=?) "
		args = append(args, before, room)
	}
	rows, err := d.Read.QueryContext(ctx, query+"ORDER BY m.created_at DESC LIMIT 40", args...)
	if err != nil {
		return nil, err
	}
	messages, err := scanMessages(rows)
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, err
}

// CreateMessage mirrors Message and Room callbacks in reference/reference/app/models.
// Publication and job delivery happen only after this transaction commits.
func (d *DB) CreateMessage(
	ctx context.Context,
	user, room int64,
	client, body, plain string,
) (Message, error) {
	return d.CreateMessageWithBlob(ctx, user, room, client, body, plain, 0)
}

func (d *DB) CreateMessageWithBlob(
	ctx context.Context,
	user, room int64,
	client, body, plain string,
	blob int64,
) (Message, error) {
	return d.createMessage(ctx, user, room, client, &body, plain, blob, nil, true)
}

// CreateWebhookReply is called only by a queued, authorized webhook delivery. Like
// the reference model callback it does not reapply controller membership checks.
func (d *DB) CreateWebhookReply(
	ctx context.Context,
	user, room int64,
	body, plain string,
	blob int64,
) (Message, error) {
	return d.createMessage(ctx, user, room, "", &body, plain, blob, nil, false)
}

// BlobStager keeps file copying outside the SQLite writer while committing the blob
// and its owning record together.
type BlobStager interface {
	Insert(context.Context, *sql.Tx) (int64, error)
	Keep()
	Discard()
}

func (d *DB) CreateMessageWithUpload(
	ctx context.Context,
	user, room int64,
	client string,
	body *string,
	plain string,
	staged BlobStager,
	webhook bool,
) (Message, error) {
	return d.createMessage(ctx, user, room, client, body, plain, 0, staged, !webhook)
}

func (d *DB) createMessage(
	ctx context.Context,
	user, room int64,
	client string,
	body *string,
	plain string,
	blob int64,
	staged BlobStager,
	checkMembership bool,
) (Message, error) {
	if staged != nil {
		defer staged.Discard()
	}
	if client == "" {
		client = uuid.NewV4().String()
	}
	now := d.Now().UTC()
	m := Message{RoomID: room, CreatorID: user, ClientID: client, CreatedAt: now, UpdatedAt: now}
	if body != nil {
		m.Body = *body
	}
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		if checkMembership {
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.room_id=? AND m.user_id=? AND u.status=0", room, user).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return ErrForbidden
			}
		}
		if err := tx.QueryRowContext(ctx, "SELECT name FROM users WHERE id=?", user).Scan(&m.Creator); err != nil {
			return err
		}
		if staged != nil {
			var err error
			blob, err = staged.Insert(ctx, tx)
			if err != nil {
				return err
			}
		}
		stamp := Stamp(now)
		r, err := tx.ExecContext(
			ctx,
			"INSERT INTO messages(client_message_id,creator_id,room_id,created_at,updated_at) VALUES (?,?,?,?,?)",
			client,
			user,
			room,
			stamp,
			stamp,
		)
		if err != nil {
			return err
		}
		m.ID, err = r.LastInsertId()
		if err != nil {
			return err
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"UPDATE rooms SET updated_at=? WHERE id=?", []any{stamp, room}},
			{"UPDATE memberships SET unread_at=?,updated_at=? WHERE room_id=? AND user_id!=? AND involvement!='invisible' AND (connected_at IS NULL OR connected_at < ?)", []any{stamp, stamp, room, user, Stamp(now.Add(-60 * time.Second))}},
			{"INSERT INTO message_search_index(rowid,body) VALUES (?,?)", []any{m.ID, plain}},
		} {
			if _, err = tx.ExecContext(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		if body != nil {
			if _, err = tx.ExecContext(ctx, "INSERT INTO action_text_rich_texts(name,record_type,record_id,body,created_at,updated_at) VALUES ('body','Message',?,?,?,?)", m.ID, *body, stamp, stamp); err != nil {
				return err
			}
		}
		if blob != 0 {
			if _, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'Message',?,'attachment',?)", blob, m.ID, stamp); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && staged != nil {
		staged.Keep()
	}
	return m, err
}

// AuthorizedSessions checks a publication's distinct sessions in one snapshot.
// json_each keeps the SQL shape stable and avoids SQLite's placeholder limit.
func (d *DB) AuthorizedSessions(
	ctx context.Context,
	tokens []string,
	room int64,
) (map[string]int64, error) {
	raw, err := json.Marshal(tokens)
	if err != nil {
		return nil, err
	}
	query := "SELECT s.token,s.user_id FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.status=0 AND s.token IN (SELECT value FROM json_each(?))"
	args := []any{string(raw)}
	if room != 0 {
		query += " AND EXISTS (SELECT 1 FROM memberships m WHERE m.user_id=s.user_id AND m.room_id=?)"
		args = append(args, room)
	}
	rows, err := d.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int64, len(tokens))
	for rows.Next() {
		var token string
		var user int64
		if err := rows.Scan(&token, &user); err != nil {
			return nil, err
		}
		result[token] = user
	}
	return result, rows.Err()
}
