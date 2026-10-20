package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

type AccountInput struct {
	Name, Styles *string
	Restrict     *sql.NullBool
	ResetJoin    bool
	Logo         *AttachmentInput
}

type AccountCommit struct {
	Account
	AttachmentCommit
}

func administratorTx(ctx context.Context, tx *sql.Tx, actor int64) error {
	var allowed bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND role=1 AND status=0)", actor).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func (d *DB) UpdateAccount(ctx context.Context, actor int64, input AccountInput) (AccountCommit, error) {
	var commit AccountCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		if err := administratorTx(ctx, tx, actor); err != nil {
			return err
		}
		var id int64
		var settings string
		if err := tx.QueryRowContext(ctx, "SELECT id,coalesce(settings,'{}') FROM accounts ORDER BY id LIMIT 1").Scan(&id, &settings); err != nil {
			return err
		}
		sets := []string{"updated_at=?"}
		args := []any{Stamp(d.Now())}
		if input.Name != nil {
			sets = append(sets, "name=?")
			args = append(args, *input.Name)
		}
		if input.Styles != nil {
			sets = append(sets, "custom_styles=?")
			args = append(args, *input.Styles)
		}
		if input.Restrict != nil {
			var data map[string]any
			if json.Unmarshal([]byte(settings), &data) != nil || data == nil {
				data = map[string]any{}
			}
			data["restrict_room_creation_to_administrators"] = nil
			if input.Restrict.Valid {
				data["restrict_room_creation_to_administrators"] = input.Restrict.Bool
			}
			b, err := json.Marshal(data)
			if err != nil {
				return err
			}
			sets = append(sets, "settings=?")
			args = append(args, string(b))
		}
		if input.ResetJoin {
			sets = append(sets, "join_code=?")
			args = append(args, RandomToken(24))
		}
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, "UPDATE accounts SET "+strings.Join(sets, ",")+" WHERE id=?", args...); err != nil {
			return err
		}
		var err error
		commit.AttachmentCommit, err = d.assignAttachment(ctx, tx, "Account", id, "logo", input.Logo)
		if err != nil {
			return err
		}
		var raw string
		err = tx.QueryRowContext(ctx, "SELECT id,name,join_code,coalesce(custom_styles,''),coalesce(settings,'{}'),updated_at,EXISTS(SELECT 1 FROM active_storage_attachments WHERE record_type='Account' AND record_id=accounts.id AND name='logo') FROM accounts WHERE id=?", id).Scan(&commit.ID, &commit.Name, &commit.JoinCode, &commit.CustomStyles, &raw, timestamp{&commit.UpdatedAt}, &commit.HasLogo)
		commit.Settings = json.RawMessage(raw)
		return err
	})
	if err != nil {
		return AccountCommit{}, err
	}
	return commit, nil
}
