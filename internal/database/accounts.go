package database

import (
	"context"
	"crypto/rand"
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
	// Whether custom_styles is set at all (AccountFirst); an empty string still renders a tag.
	HasCustomStyles bool
}

func (d *DB) Account(ctx context.Context) (Account, error) {
	var a Account
	var settings string
	err := d.Read.QueryRowContext(ctx, "SELECT id,name,join_code,coalesce(custom_styles,''),coalesce(settings,'{}'),updated_at,EXISTS(SELECT 1 FROM active_storage_attachments WHERE record_type='Account' AND record_id=accounts.id AND name='logo') FROM accounts ORDER BY id LIMIT 1").Scan(&a.ID, &a.Name, &a.JoinCode, &a.CustomStyles, &settings, timestamp{&a.UpdatedAt}, &a.HasLogo)
	a.Settings = json.RawMessage(settings)
	return a, err
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
func (d *DB) CreateUser(ctx context.Context, name, email, password, bio string, role int, webhook *string, uploads ...BlobStager) (User, error) {
	var u User
	err := d.recordWithUpload(ctx, "User", &u.ID, uploads, func(tx *Tx) error {
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
	return u, err
}
func (d *DB) UpdateUser(ctx context.Context, id int64, attributes map[string]string, webhook *string, uploads ...BlobStager) error {
	return d.recordWithUpload(ctx, "User", &id, uploads, func(tx *Tx) error {
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
			return ErrNoRows
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
}
func (d *DB) DeactivateUser(ctx context.Context, id int64) error {
	return d.Transaction(ctx, func(tx *Tx) error {
		now := Stamp(d.Now())
		var email NullString
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
}
func (d *DB) BanUser(ctx context.Context, id int64, ban bool) error {
	err := d.Transaction(ctx, func(tx *Tx) error {
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
	return d.exists(ctx, `SELECT 1 AS one FROM "bans" WHERE "bans"."ip_address" = ? LIMIT 1`, ip)
}
func (u User) BotKey() string { return fmt.Sprintf("%d-%s", u.ID, u.BotToken) }
