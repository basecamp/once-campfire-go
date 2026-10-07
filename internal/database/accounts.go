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
	d.syncExternal(ctx)
	if a, ok := d.cachedAccount(); ok {
		return a, nil
	}
	var a Account
	var settings string
	err := d.Read.QueryRowContext(ctx, "SELECT id,name,join_code,coalesce(custom_styles,''),coalesce(settings,'{}'),updated_at,EXISTS(SELECT 1 FROM active_storage_attachments WHERE record_type='Account' AND record_id=accounts.id AND name='logo') FROM accounts ORDER BY id LIMIT 1").Scan(&a.ID, &a.Name, &a.JoinCode, &a.CustomStyles, &settings, timestamp{&a.UpdatedAt}, &a.HasLogo)
	if err != nil {
		return a, err
	}
	a.Settings = json.RawMessage(settings)
	d.storeAccount(a)
	return a, nil
}
func (a Account) RestrictRooms() bool {
	var s struct {
		Restrict bool `json:"restrict_room_creation_to_administrators"`
	}
	json.Unmarshal(a.Settings, &s)
	return s.Restrict
}
func (d *DB) UpdateAccount(ctx context.Context, name *string, styles *string, restrict *bool, resetJoin bool, uploads ...BlobStager) error {
	var id int64
	err := d.recordWithUpload(ctx, "Account", &id, uploads, func(tx *sql.Tx) error {
		var settings string
		if err := tx.QueryRowContext(ctx, "SELECT id,coalesce(settings,'{}') FROM accounts ORDER BY id LIMIT 1").Scan(&id, &settings); err != nil {
			return err
		}
		sets := []string{"updated_at=?"}
		args := []any{Stamp(d.Now())}
		if name != nil {
			sets = append(sets, "name=?")
			args = append(args, *name)
		}
		if styles != nil {
			sets = append(sets, "custom_styles=?")
			args = append(args, *styles)
		}
		if restrict != nil {
			var data map[string]any
			if json.Unmarshal([]byte(settings), &data) != nil || data == nil {
				data = map[string]any{}
			}
			data["restrict_room_creation_to_administrators"] = *restrict
			b, err := json.Marshal(data)
			if err != nil {
				return err
			}
			sets = append(sets, "settings=?")
			args = append(args, string(b))
		}
		if resetJoin {
			sets = append(sets, "join_code=?")
			args = append(args, RandomToken(24))
		}
		args = append(args, id)
		_, err := tx.ExecContext(ctx, "UPDATE accounts SET "+strings.Join(sets, ",")+" WHERE id=?", args...)
		return err
	})
	if err == nil {
		d.clearAccount()
		d.changed()
	}
	return err
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
	return userRow(d.Read.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users u WHERE u.id=?", id))
}
func usersRows(rows *sql.Rows) ([]User, error) {
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Password, &u.Role, &u.Status, &u.Bio, timestamp{&u.UpdatedAt}, &u.BotToken); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
func (d *DB) Users(ctx context.Context, room int64, botsOnly bool) ([]User, error) {
	query := "SELECT " + userColumns + " FROM users u "
	args := []any{}
	if room != 0 {
		query += "JOIN memberships m ON m.user_id=u.id AND m.room_id=? "
		args = append(args, room)
	}
	query += "WHERE u.status=0 "
	if botsOnly {
		query += "AND u.role=2 "
	}
	rows, err := d.Read.QueryContext(ctx, query+"ORDER BY lower(u.name)", args...)
	if err != nil {
		return nil, err
	}
	return usersRows(rows)
}
func (d *DB) CreateUser(ctx context.Context, name, email, password, bio string, role int, webhook *string, uploads ...BlobStager) (User, error) {
	var u User
	err := d.recordWithUpload(ctx, "User", &u.ID, uploads, func(tx *sql.Tx) error {
		now := Stamp(d.Now())
		var address, digest, bot any = email, password, nil
		if role == 2 {
			address = nil
			digest = nil
			bot = RandomToken(12)
		}
		r, err := tx.ExecContext(ctx, "INSERT INTO users(name,email_address,password_digest,bio,role,status,bot_token,created_at,updated_at) VALUES (?,?,?,?,?,0,?,?,?)", name, address, digest, bio, role, bot, now, now)
		if err != nil {
			return err
		}
		id, err := r.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO memberships(room_id,user_id,created_at,updated_at) SELECT id,?,?,? FROM rooms WHERE type='Rooms::Open'", id, now, now); err != nil {
			return err
		}
		if role == 2 && webhook != nil {
			if _, err = tx.ExecContext(ctx, "INSERT INTO webhooks(user_id,url,created_at,updated_at) VALUES (?,?,?,?)", id, *webhook, now, now); err != nil {
				return err
			}
		}
		u = User{ID: id, Name: name, Email: email, Password: password, Role: role, Bio: bio, UpdatedAt: d.Now()}
		if bot != nil {
			u.BotToken = bot.(string)
		}
		return nil
	})
	if err == nil {
		d.changed()
	}
	return u, err
}
func (d *DB) UpdateUser(ctx context.Context, id int64, attributes map[string]string, webhook *string, uploads ...BlobStager) error {
	err := d.recordWithUpload(ctx, "User", &id, uploads, func(tx *sql.Tx) error {
		sets := []string{"updated_at=?"}
		args := []any{Stamp(d.Now())}
		for _, key := range []string{"name", "email_address", "password_digest", "bio", "role", "bot_token"} {
			if value, ok := attributes[key]; ok {
				sets = append(sets, key+"=?")
				args = append(args, value)
			}
		}
		args = append(args, id)
		r, err := tx.ExecContext(ctx, "UPDATE users SET "+strings.Join(sets, ",")+" WHERE id=?", args...)
		if err != nil {
			return err
		}
		count, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return sql.ErrNoRows
		}
		if webhook != nil {
			if strings.TrimSpace(*webhook) == "" {
				_, err = tx.ExecContext(ctx, "DELETE FROM webhooks WHERE user_id=?", id)
			} else {
				var exists int
				err = tx.QueryRowContext(ctx, "SELECT count(*) FROM webhooks WHERE user_id=?", id).Scan(&exists)
				if err != nil {
					return err
				}
				now := Stamp(d.Now())
				if exists == 0 {
					_, err = tx.ExecContext(ctx, "INSERT INTO webhooks(user_id,url,created_at,updated_at) VALUES (?,?,?,?)", id, *webhook, now, now)
				} else {
					_, err = tx.ExecContext(ctx, "UPDATE webhooks SET url=?,updated_at=? WHERE user_id=?", *webhook, now, id)
				}
			}
		}
		return err
	})
	if err == nil {
		d.changed()
	}
	return err
}
func (d *DB) Bot(ctx context.Context, key string) (User, error) {
	id, token, ok := strings.Cut(strings.TrimSpace(key), "-")
	if !ok {
		return User{}, sql.ErrNoRows
	}
	return userRow(d.Read.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users u WHERE u.id=? AND u.bot_token=? AND u.role=2 AND u.status=0", id, token))
}
func (d *DB) DeactivateUser(ctx context.Context, id int64) error {
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		now := Stamp(d.Now())
		var email sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT email_address FROM users WHERE id=?", id).Scan(&email); err != nil {
			return err
		}
		var address any
		if email.Valid {
			address = strings.ReplaceAll(email.String, "@", "-deactivated-"+UUID()+"@")
		}
		for _, table := range []string{"push_subscriptions", "searches", "sessions"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE user_id=?", id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM memberships WHERE user_id=? AND room_id IN (SELECT id FROM rooms WHERE type!='Rooms::Direct')", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE users SET status=1,email_address=?,updated_at=? WHERE id=?", address, now, id)
		return err
	})
	if err == nil {
		d.changed()
	}
	return err
}
func (d *DB) BanUser(ctx context.Context, id int64, ban bool) error {
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		now := Stamp(d.Now())
		status := 0
		if ban {
			status = 2
			if _, err := tx.ExecContext(ctx, "INSERT INTO bans(user_id,ip_address,created_at,updated_at) SELECT DISTINCT user_id,ip_address,?,? FROM sessions WHERE user_id=? AND ip_address IS NOT NULL AND trim(ip_address)!=''", now, now, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", id); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, "DELETE FROM bans WHERE user_id=?", id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE users SET status=?,updated_at=? WHERE id=?", status, now, id)
		return err
	})
	if err == nil && ban && d.RemoveBannedContent != nil {
		d.RemoveBannedContent(id)
	}
	return err
}
func (d *DB) BannedIP(ctx context.Context, ip string) (bool, error) {
	var n int
	err := d.Read.QueryRowContext(ctx, "SELECT count(*) FROM bans WHERE ip_address=?", ip).Scan(&n)
	return n > 0, err
}
func (d *DB) RefreshSession(ctx context.Context, token, agent, ip string) (bool, error) {
	now := d.Now()
	var active time.Time
	if err := d.Read.QueryRowContext(ctx, "SELECT last_active_at FROM sessions WHERE token=?", token).Scan(timestamp{&active}); err != nil {
		return false, err
	}
	if !active.Before(now.Add(-time.Hour)) {
		return false, nil
	}
	r, err := d.Write.ExecContext(ctx, "UPDATE sessions SET last_active_at=?,updated_at=?,user_agent=?,ip_address=? WHERE token=? AND last_active_at<?", Stamp(now), Stamp(now), agent, ip, token, Stamp(now.Add(-time.Hour)))
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}
func (u User) BotKey() string { return fmt.Sprintf("%d-%s", u.ID, u.BotToken) }

func (d *DB) AccountUsers(ctx context.Context, includeBanned bool) ([]User, error) {
	status := "u.status=0"
	if includeBanned {
		status = "u.status IN (0,2)"
	}
	rows, err := d.Read.QueryContext(ctx, "SELECT "+userColumns+" FROM users u WHERE "+status+" AND u.role != 2 ORDER BY lower(u.name)")
	if err != nil {
		return nil, err
	}
	return usersRows(rows)
}

func (d *DB) RoomMembers(ctx context.Context, room int64) ([]User, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT "+userColumns+" FROM users u JOIN memberships m ON m.user_id=u.id WHERE m.room_id=?", room)
	if err != nil {
		return nil, err
	}
	return usersRows(rows)
}

func (d *DB) DirectPlaceholders(ctx context.Context, user int64) ([]User, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT DISTINCT user_id FROM memberships WHERE room_id IN (SELECT r.id FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE r.type='Rooms::Direct' AND m.user_id=?)", user)
	if err != nil {
		return nil, err
	}
	ids := []any{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	ids = append(ids, user)
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err = d.Read.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM users u WHERE u.status=0 AND u.id NOT IN (%s) ORDER BY u.created_at ASC LIMIT %d", userColumns, marks, max(0, 20-len(ids))), ids...)
	if err != nil {
		return nil, err
	}
	return usersRows(rows)
}
