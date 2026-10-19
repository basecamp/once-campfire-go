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
	if err == nil {
		// A sessions-table write moves the authorization generation, keeping
		// the ENGINE-40b audit contract "every session insert/delete bumps".
		d.bumpSessionVersion()
	}
	return token, err
}

// DeleteSession removes one session row (logout). It bumps the session
// generation so no cached publication authorization for the token can
// outlive the deletion; callers that revoke a session must go through this
// helper rather than writing the sessions table directly.
func (d *DB) DeleteSession(ctx context.Context, token string, user int64) error {
	r, err := d.Write.ExecContext(ctx, "DELETE FROM sessions WHERE token=? AND user_id=?", token, user)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		d.bumpSessionVersion()
	}
	return nil
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
	if err == nil {
		// Setup creates the owner, the open room and its membership: the
		// sidebar and the search membership scope both change.
		d.bumpSidebarVersion()
		d.membershipVersion.Add(1)
	}
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
// and its owning record together. Insert runs inside the record's transaction,
// through UploadTx: *sql.Tx on the database/sql paths, the direct lane's
// transaction on the fastdb path.
type BlobStager interface {
	Insert(context.Context, UploadTx) (int64, error)
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
	// run executes the statements that commit together with the message row:
	// the authorized creator lookup (membership check and name together),
	// staged blob, message, room touch, body, attachment, FTS and unread.
	// The insert trigger records durable push intent in that same transaction.
	// A process crash cannot leave primary data without its derived state.
	run := func(tx jobTx) (Message, error) {
		created := m
		// One statement for the authorized creator: the membership check and
		// the name lookup together (ENGINE-45 cut). The webhook path skips the
		// membership check and reads the name alone, so a missing or inactive
		// user stays sql.ErrNoRows there while the membership path reports
		// ErrForbidden — the behaviour the two separate reads had.
		var name string
		var err error
		if checkMembership {
			name, err = tx.AuthorizedCreatorName(ctx, room, user)
		} else {
			name, err = tx.CreatorName(ctx, user)
		}
		if err != nil {
			return created, err
		}
		created.Creator = name
		if staged != nil {
			blob, err = staged.Insert(ctx, tx)
			if err != nil {
				return created, err
			}
		}
		stamp := Stamp(now)
		created.ID, err = tx.InsertMessage(ctx, client, user, room, stamp)
		if err != nil {
			return created, err
		}
		if err := tx.TouchRoom(ctx, room, stamp); err != nil {
			return created, err
		}
		if body != nil {
			if err := tx.InsertRichText(ctx, created.ID, *body, stamp); err != nil {
				return created, err
			}
		}
		if blob != 0 {
			if err := tx.InsertAttachment(ctx, blob, created.ID, stamp); err != nil {
				return created, err
			}
		}
		if err := tx.InsertSearchIndex(ctx, created.ID, plain); err != nil {
			return created, err
		}
		if err := tx.BumpUnread(ctx, room, user, stamp, Stamp(now.Add(-60*time.Second))); err != nil {
			return created, err
		}
		return created, nil
	}
	if d.writer == nil {
		err := d.Transaction(ctx, func(tx *sql.Tx) error {
			lane := &sqlLaneTx{tx: tx}
			var err error
			if m, err = run(lane); err != nil {
				return err
			}
			return nil
		})
		if err == nil {
			// ENGINE-20/ENGINE-30 registries: bump after the commit so no
			// reader can see the new version with old rows.
			d.bumpSidebarVersion()
			d.corpusVersion.Add(1)
		}
		if err == nil && staged != nil {
			staged.Keep()
		}
		return m, err
	}
	// Both queued group commit and the idle in-line path commit the whole
	// message atomically. Network publication is best effort; push is replayed
	// independently from the durable outbox.
	job := &messageJob{
		ctx:  ctx,
		run:  run,
		done: make(chan messageResult, 1),
	}
	// The commit gate: this caller counts toward Close's drain until its job
	// has been answered (the writer's after phase included), so Close never
	// tears the lane down under a caller that is still finishing its create.
	// The defer covers every exit, including a panic in the caller's tail.
	d.afterMu.Lock()
	d.afterN++
	d.afterMu.Unlock()
	defer func() {
		d.afterMu.Lock()
		d.afterN--
		if d.afterN == 0 {
			d.afterCV.Broadcast()
		}
		d.afterMu.Unlock()
	}()
	if err := d.writer.submit(job); err != nil {
		return Message{}, err
	}
	result := <-job.done
	if result.err != nil {
		return Message{}, result.err
	}
	// The shared commit landed; the ENGINE-20/ENGINE-30 registries must move
	// with it so no reader serves a version-keyed cache that hides the
	// committed message. FTS, unread state and push intent commit atomically
	// with the row on both lanes; these bumps follow that primary commit.
	d.bumpSidebarVersion()
	d.corpusVersion.Add(1)
	if staged != nil {
		staged.Keep()
	}
	// The message row is committed; an after-commit failure is reported the
	// way the queued path always reported it — the message stays.
	return result.message, result.afterErr
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

// RoomMembersAmong appends to dst the members of room among userIDs, in one
// snapshot, and returns the extended slice. It is the membership recheck a
// publication runs after its candidate read: the candidate read established
// membership at an earlier instant, so a member removed in between must be
// recognised here and skipped rather than notified. json_each keeps the SQL
// shape stable and avoids SQLite's placeholder limit.
//
// The result is a caller-owned slice the caller sorts and binary-searches, not
// a map built per call: the recheck runs on every unread bump, and the map was
// its only avoidable container (the row destinations and the JSON bind string
// are database/sql's and the encoder's).
func (d *DB) RoomMembersAmong(ctx context.Context, dst []int64, room int64, userIDs []int64) ([]int64, error) {
	raw, err := json.Marshal(userIDs)
	if err != nil {
		return dst, err
	}
	rows, err := d.Read.QueryContext(ctx, "SELECT user_id FROM memberships WHERE room_id=? AND user_id IN (SELECT value FROM json_each(?))", room, string(raw))
	if err != nil {
		return dst, err
	}
	defer rows.Close()
	for rows.Next() {
		var user int64
		if err := rows.Scan(&user); err != nil {
			return dst, err
		}
		dst = append(dst, user)
	}
	return dst, rows.Err()
}
