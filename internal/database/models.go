package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrForbidden = errors.New("forbidden")
var ErrValidation = errors.New("invalid attributes")

type User struct {
	ID                    int64
	Name, Email, Password string
	Bio, BotToken         string
	UpdatedAt             time.Time
	Role, Status          int
	// Whether email_address / bio are NULL (set by ScanReferenceUser).
	NullEmail, NullBio bool
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
func UUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

const userColumns = "u.id,u.name,coalesce(u.email_address,''),coalesce(u.password_digest,''),u.role,u.status,coalesce(u.bio,''),u.updated_at,coalesce(u.bot_token,'')"

func userRow(row *Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.Password, &u.Role, &u.Status, &u.Bio, timestamp{&u.UpdatedAt}, &u.BotToken)
	return u, err
}
func (d *DB) UserByEmail(ctx context.Context, email string) (User, error) {
	return userRow(d.Read.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users u WHERE u.email_address=? AND u.status=0", email))
}
func (d *DB) SessionUser(ctx context.Context, token string) (User, error) {
	return userRow(d.Read.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users u JOIN sessions s ON s.user_id=u.id WHERE s.token=? AND u.status=0", token))
}
func (d *DB) StartSession(ctx context.Context, user int64, agent, ip string) (string, error) {
	token, now := Token(), Stamp(d.Now())
	_, err := d.Write.ExecContext(ctx, "INSERT INTO sessions(token,user_id,user_agent,ip_address,last_active_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?)", token, user, agent, ip, now, now, now)
	return token, err
}
func (d *DB) Setup(ctx context.Context, name, email, passwordDigest string, uploads ...BlobStager) (User, error) {
	var u User
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" || passwordDigest == "" {
		return u, ErrValidation
	}
	err := d.recordWithUpload(ctx, "User", &u.ID, uploads, func(tx *Tx) error {
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
		r, err := tx.ExecContext(ctx, "INSERT INTO users(name,email_address,password_digest,role,status,created_at,updated_at) VALUES (?,?,?,1,0,?,?)", name, email, passwordDigest, now, now)
		if err != nil {
			return err
		}
		id, err := r.LastInsertId()
		if err != nil {
			return err
		}
		r, err = tx.ExecContext(ctx, "INSERT INTO rooms(name,type,creator_id,created_at,updated_at) VALUES (?,'Rooms::Open',?,?,?)", "All Talk", id, now, now)
		if err != nil {
			return err
		}
		room, err := r.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO memberships(room_id,user_id,created_at,updated_at) VALUES (?,?,?,?)", room, id, now, now)
		u = User{ID: id, Name: name, Email: email, Role: 1}
		return err
	})
	return u, err
}
func (d *DB) Rooms(ctx context.Context, user int64) ([]Room, error) { return d.rooms(ctx, user, true) }
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
	err := d.Read.QueryRowContext(ctx, "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE m.user_id=? AND r.id=?", user, id).Scan(&r.ID, &r.CreatorID, &r.Name, &r.Type, timestamp{&r.UpdatedAt})
	return r, err
}

const messageSelect = "SELECT m.id,m.room_id,m.creator_id,m.client_message_id,coalesce(t.body,''),coalesce(u.name,''),m.created_at,m.updated_at FROM messages m LEFT JOIN users u ON u.id=m.creator_id LEFT JOIN action_text_rich_texts t ON t.record_type='Message' AND t.record_id=m.id AND t.name='body' "

func scanMessages(rows *Rows) ([]Message, error) {
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
func (d *DB) CreateMessage(ctx context.Context, user, room int64, client, body, plain string) (Message, error) {
	return d.CreateMessageWithBlob(ctx, user, room, client, body, plain, 0)
}
func (d *DB) CreateMessageWithBlob(ctx context.Context, user, room int64, client, body, plain string, blob int64) (Message, error) {
	return d.createMessage(ctx, user, room, client, &body, plain, blob, nil, true)
}

// CreateWebhookReply is called only by a queued, authorized webhook delivery. Like
// the reference model callback it does not reapply controller membership checks.
func (d *DB) CreateWebhookReply(ctx context.Context, user, room int64, body, plain string, blob int64) (Message, error) {
	return d.createMessage(ctx, user, room, "", &body, plain, blob, nil, false)
}

// BlobStager keeps file copying outside the SQLite writer while committing the blob
// and its owning record together.
type BlobStager interface {
	Insert(context.Context, *Tx) (int64, error)
	Keep()
	Discard()
}

func (d *DB) CreateMessageWithUpload(ctx context.Context, user, room int64, client string, body *string, plain string, staged BlobStager, webhook bool) (Message, error) {
	return d.createMessage(ctx, user, room, client, body, plain, 0, staged, !webhook)
}
func (d *DB) createMessage(ctx context.Context, user, room int64, client string, body *string, plain string, blob int64, staged BlobStager, _ bool) (Message, error) {
	if staged != nil {
		defer staged.Discard()
	}
	if client == "" {
		client = UUID()
	}
	now := d.Now().UTC()
	m := Message{RoomID: room, CreatorID: user, ClientID: client, CreatedAt: now, UpdatedAt: now}
	if body != nil {
		m.Body = *body
	}
	// The reference's Message::create: inside the transaction the message, its body (which
	// touches the message), its attachment and the room touch; after commit the search index,
	// then Room::receive's unread memberships. Callers have already checked the room membership,
	// as the reference's controllers do before writing.
	err := d.Transaction(ctx, func(tx *Tx) error {
		if staged != nil {
			var err error
			blob, err = staged.Insert(ctx, tx)
			if err != nil {
				return err
			}
		}
		stamp := Stamp(now)
		r, err := tx.ExecContext(ctx, "INSERT INTO messages(client_message_id,created_at,creator_id,room_id,updated_at) VALUES (?,?,?,?,?)", client, stamp, user, room, stamp)
		if err != nil {
			return err
		}
		m.ID, err = r.LastInsertId()
		if err != nil {
			return err
		}
		touched := false
		if body != nil {
			if _, err = tx.ExecContext(ctx, "INSERT INTO action_text_rich_texts(body,created_at,name,record_id,record_type,updated_at) VALUES (?,?,'body',?,'Message',?)", *body, stamp, m.ID, stamp); err != nil {
				return err
			}
			touched = true
		}
		if blob != 0 {
			if _, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'Message',?,'attachment',?)", blob, m.ID, stamp); err != nil {
				return err
			}
			touched = true
		}
		if touched {
			if _, err = tx.ExecContext(ctx, "UPDATE messages SET updated_at=? WHERE id=?", stamp, m.ID); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE rooms SET updated_at=? WHERE id=?", stamp, room); err != nil {
			return err
		}
		id := m.ID
		tx.AfterCommit(func(tx *Tx) error {
			text, err := d.indexedText(tx, id, plain)
			if err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO message_search_index(rowid,body) VALUES (?,?)", id, text); err != nil {
				return err
			}
			cutoff := Stamp(d.Now().Add(-60 * time.Second))
			_, err = tx.Exec("UPDATE memberships SET unread_at=?,updated_at=? WHERE room_id=? AND involvement!='invisible' AND (connected_at IS NULL OR connected_at < ?) AND user_id!=?", stamp, Stamp(d.Now()), room, cutoff, user)
			return err
		})
		return nil
	})
	if err == nil && staged != nil {
		staged.Keep()
	}
	return m, err
}

// indexedText is the reference's Message#plain_text_body, read on the writer after commit: the
// stored body's plain text, else the attachment's filename. Without a PlainText converter (the
// database package's own tests) it's the caller's plain text.
func (d *DB) indexedText(tx *Tx, id int64, plain string) (string, error) {
	if d.PlainText == nil {
		return plain, nil
	}
	var body NullString
	err := tx.QueryRow("SELECT body FROM action_text_rich_texts WHERE record_type='Message' AND record_id=? AND name='body' LIMIT 1", id).Scan(&body)
	if err != nil && err != ErrNoRows {
		return "", err
	}
	if body.Valid {
		if text := d.PlainText(body.String); strings.TrimSpace(text) != "" {
			return text, nil
		}
	}
	var filename string
	err = tx.QueryRow("SELECT b.filename FROM active_storage_attachments a JOIN active_storage_blobs b ON b.id=a.blob_id WHERE a.record_type='Message' AND a.record_id=? AND a.name='attachment' LIMIT 1", id).Scan(&filename)
	if err == ErrNoRows {
		return "", nil
	}
	return filename, err
}
func (d *DB) Search(ctx context.Context, user int64, query string) ([]Message, error) {
	words := strings.Fields(SearchQuery(query))
	if len(words) == 0 {
		return []Message{}, nil
	}
	for i, w := range words {
		words[i] = "\"" + strings.ReplaceAll(w, "\"", "\"\"") + "\""
	}
	rows, err := d.Read.QueryContext(ctx, messageSelect+"JOIN message_search_index idx ON idx.rowid=m.id JOIN memberships member ON member.room_id=m.room_id WHERE member.user_id=? AND idx.body MATCH ? ORDER BY m.created_at DESC LIMIT 100", user, strings.Join(words, " "))
	if err != nil {
		return nil, err
	}
	messages, err := scanMessages(rows)
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, err
}

// AuthorizedSessions checks a publication's distinct sessions in one snapshot.
// json_each keeps the SQL shape stable and avoids SQLite's placeholder limit.
func (d *DB) AuthorizedSessions(ctx context.Context, tokens []string, room int64) (map[string]int64, error) {
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
