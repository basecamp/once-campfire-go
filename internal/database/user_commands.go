package database

import (
	"context"
	"database/sql"
	"strings"
	"uuid"
)

type UserInput struct {
	Name, Email, Password, Bio string
	Role                       int
	Webhook                    *string
	Avatar                     *AttachmentInput
}

type UserChanges struct {
	Name, Email, Password, Bio, BotToken *string
	Role                                 *int
	Webhook                              *string
	Avatar                               *AttachmentInput
}

type UserCommit struct {
	User
	AttachmentCommit
}

func captureUser(ctx context.Context, tx *sql.Tx, id int64) (User, error) {
	return userRow(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users u WHERE u.id=?", id))
}

func (d *DB) Setup(ctx context.Context, name, email, passwordDigest string, avatars ...*AttachmentInput) (UserCommit, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" || passwordDigest == "" {
		return UserCommit{}, ErrValidation
	}
	var avatar *AttachmentInput
	if len(avatars) > 0 {
		avatar = avatars[0]
	}
	var commit UserCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return ErrForbidden
		}
		now := Stamp(d.Now())
		if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(name,join_code,settings,created_at,updated_at) VALUES (?,?,?,?,?)", "Campfire", Token(), "{}", now, now); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "INSERT INTO users(name,email_address,password_digest,role,status,created_at,updated_at) VALUES (?,?,?,1,0,?,?)", name, email, passwordDigest, now, now)
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		user, err := captureUser(ctx, tx, id)
		if err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, "INSERT INTO rooms(name,type,creator_id,created_at,updated_at) VALUES (?,'Rooms::Open',?,?,?)", "All Talk", user.ID, now, now)
		if err != nil {
			return err
		}
		room, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO memberships(room_id,user_id,created_at,updated_at) VALUES (?,?,?,?)", room, user.ID, now, now); err != nil {
			return err
		}
		commit.User = user
		commit.AttachmentCommit, err = d.assignAttachment(ctx, tx, "User", user.ID, "avatar", avatar)
		return err
	})
	if err != nil {
		return UserCommit{}, err
	}
	return commit, nil
}

// CreateUser is administrator-authorized creation. JoinUser owns capability/IP
// checks for signup; no HTTP snapshot or authorization bypass enters either path.
func (d *DB) CreateUser(ctx context.Context, actor int64, input UserInput) (UserCommit, error) {
	var commit UserCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		if err := administratorTx(ctx, tx, actor); err != nil {
			return err
		}
		var err error
		commit.User, err = d.insertUser(ctx, tx, input)
		if err != nil {
			return err
		}
		commit.AttachmentCommit, err = d.assignAttachment(ctx, tx, "User", commit.ID, "avatar", input.Avatar)
		return err
	})
	if err != nil {
		return UserCommit{}, err
	}
	return commit, nil
}

func (d *DB) JoinUser(ctx context.Context, code, ip string, input UserInput) (UserCommit, error) {
	var commit UserCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		var current string
		if err := tx.QueryRowContext(ctx, "SELECT join_code FROM accounts ORDER BY id LIMIT 1").Scan(&current); err != nil {
			return err
		}
		if code != current {
			return sql.ErrNoRows
		}
		var banned bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM bans WHERE ip_address=?)", ip).Scan(&banned); err != nil {
			return err
		}
		if banned {
			return ErrForbidden
		}
		if input.Role != 0 || input.Webhook != nil {
			return ErrValidation
		}
		var err error
		commit.User, err = d.insertUser(ctx, tx, input)
		if err != nil {
			return err
		}
		commit.AttachmentCommit, err = d.assignAttachment(ctx, tx, "User", commit.ID, "avatar", input.Avatar)
		return err
	})
	if err != nil {
		return UserCommit{}, err
	}
	return commit, nil
}

func (d *DB) insertUser(ctx context.Context, tx *sql.Tx, input UserInput) (User, error) {
	now := Stamp(d.Now())
	var email, password, bot any = input.Email, input.Password, nil
	if input.Role == 2 {
		email = nil
		password = nil
		bot = RandomToken(12)
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO users(name,email_address,password_digest,bio,role,status,bot_token,created_at,updated_at) VALUES (?,?,?,?,?,0,?,?,?)", input.Name, email, password, input.Bio, input.Role, bot, now, now)
	if err != nil {
		return User{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO memberships(room_id,user_id,created_at,updated_at) SELECT id,?,?,? FROM rooms WHERE type='Rooms::Open'", id, now, now); err != nil {
		return User{}, err
	}
	if input.Role == 2 && input.Webhook != nil {
		if _, err = tx.ExecContext(ctx, "INSERT INTO webhooks(user_id,url,created_at,updated_at) VALUES (?,?,?,?)", id, *input.Webhook, now, now); err != nil {
			return User{}, err
		}
	}
	return captureUser(ctx, tx, id)
}

func (d *DB) UpdateUser(ctx context.Context, actor, id int64, input UserChanges) (UserCommit, error) {
	return d.updateUser(ctx, actor, id, input, false)
}

func (d *DB) UpdateBot(ctx context.Context, actor, id int64, input UserChanges) (UserCommit, error) {
	return d.updateUser(ctx, actor, id, input, true)
}

func (d *DB) updateUser(ctx context.Context, actor, id int64, input UserChanges, botOnly bool) (UserCommit, error) {
	var commit UserCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		if actor != id || input.Role != nil || input.BotToken != nil || input.Webhook != nil || botOnly {
			if err := administratorTx(ctx, tx, actor); err != nil {
				return err
			}
		} else {
			var active bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND status=0)", actor).Scan(&active); err != nil {
				return err
			}
			if !active {
				return ErrForbidden
			}
		}
		u, err := captureUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if u.Status != 0 || botOnly && u.Role != 2 {
			return sql.ErrNoRows
		}
		sets := []string{"updated_at=?"}
		args := []any{Stamp(d.Now())}
		for _, field := range []struct {
			name  string
			value *string
		}{{"name", input.Name}, {"email_address", input.Email}, {"password_digest", input.Password}, {"bio", input.Bio}, {"bot_token", input.BotToken}} {
			if field.value != nil {
				sets = append(sets, field.name+"=?")
				args = append(args, *field.value)
			}
		}
		if input.Role != nil {
			sets = append(sets, "role=?")
			args = append(args, *input.Role)
		}
		args = append(args, id)
		if _, err = tx.ExecContext(ctx, "UPDATE users SET "+strings.Join(sets, ",")+" WHERE id=?", args...); err != nil {
			return err
		}
		if input.Webhook != nil {
			if strings.TrimSpace(*input.Webhook) == "" {
				_, err = tx.ExecContext(ctx, "DELETE FROM webhooks WHERE user_id=?", id)
			} else {
				var exists bool
				if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM webhooks WHERE user_id=?)", id).Scan(&exists); err != nil {
					return err
				}
				now := Stamp(d.Now())
				if exists {
					_, err = tx.ExecContext(ctx, "UPDATE webhooks SET url=?,updated_at=? WHERE user_id=?", *input.Webhook, now, id)
				} else {
					_, err = tx.ExecContext(ctx, "INSERT INTO webhooks(user_id,url,created_at,updated_at) VALUES (?,?,?,?)", id, *input.Webhook, now, now)
				}
			}
			if err != nil {
				return err
			}
		}
		commit.AttachmentCommit, err = d.assignAttachment(ctx, tx, "User", id, "avatar", input.Avatar)
		if err != nil {
			return err
		}
		commit.User, err = captureUser(ctx, tx, id)
		return err
	})
	if err != nil {
		return UserCommit{}, err
	}
	return commit, nil
}

func (d *DB) DeactivateUser(ctx context.Context, actor, id int64) error {
	return d.deactivateUser(ctx, actor, id, false)
}

func (d *DB) DeactivateBot(ctx context.Context, actor, id int64) error {
	return d.deactivateUser(ctx, actor, id, true)
}

func (d *DB) deactivateUser(ctx context.Context, actor, id int64, botOnly bool) error {
	return d.Transaction(ctx, func(tx *sql.Tx) error {
		if err := administratorTx(ctx, tx, actor); err != nil {
			return err
		}
		var email sql.NullString
		var role, status int
		if err := tx.QueryRowContext(ctx, "SELECT email_address,role,status FROM users WHERE id=?", id).Scan(&email, &role, &status); err != nil {
			return err
		}
		if status != 0 || botOnly && role != 2 {
			return sql.ErrNoRows
		}
		var address any
		if email.Valid {
			address = strings.ReplaceAll(email.String, "@", "-deactivated-"+uuid.NewV4().String()+"@")
		}
		for _, table := range []string{"push_subscriptions", "searches", "sessions"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE user_id=?", id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM memberships WHERE user_id=? AND room_id IN (SELECT id FROM rooms WHERE type!='Rooms::Direct')", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE users SET status=1,email_address=?,updated_at=? WHERE id=?", address, Stamp(d.Now()), id)
		return err
	})
}

func (d *DB) BanUser(ctx context.Context, actor, id int64, ban bool) error {
	return d.Transaction(ctx, func(tx *sql.Tx) error {
		if err := administratorTx(ctx, tx, actor); err != nil {
			return err
		}
		if _, err := captureUser(ctx, tx, id); err != nil {
			return err
		}
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
}
