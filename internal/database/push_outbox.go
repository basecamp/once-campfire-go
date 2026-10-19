package database

import (
	"context"
	"time"
)

// PendingPush appends at most limit outstanding message IDs into dst. Intent
// survives a process crash; acknowledgement follows delivery, never enqueue.
func (d *DB) PendingPush(ctx context.Context, dst []int64, limit int) ([]int64, error) {
	return d.pendingPush(ctx, dst, limit, int64(1<<63-1))
}

func (d *DB) DuePush(ctx context.Context, dst []int64, limit int) ([]int64, error) {
	return d.pendingPush(ctx, dst, limit, time.Now().Unix())
}

func (d *DB) pendingPush(ctx context.Context, dst []int64, limit int, now int64) ([]int64, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT message_id FROM campfire_push_outbox WHERE retry_at<=? ORDER BY retry_at,message_id LIMIT ?", now, limit)
	if err != nil {
		return dst, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return dst, err
		}
		dst = append(dst, id)
	}
	return dst, rows.Err()
}

// RetryPush backs off a failed delivery so a poison recipient cannot keep
// later messages outside the bounded scan window indefinitely.
func (d *DB) RetryPush(ctx context.Context, id int64) error {
	_, err := d.Write.ExecContext(ctx, "UPDATE campfire_push_outbox SET retry_at=? WHERE message_id=?", time.Now().Add(30*time.Second).Unix(), id)
	return err
}

func (d *DB) AckPush(ctx context.Context, id int64) error {
	_, err := d.Write.ExecContext(ctx, "DELETE FROM campfire_push_outbox WHERE message_id=?", id)
	return err
}

// PushProgress is one attempted recipient's durable outcome. Failed recipients
// move behind untouched work; successful recipients never restart on replay.
type PushProgress struct {
	MessageID, SubscriptionID int64
	Completed                 bool
	Gone                      bool
}

type PushOutcome struct {
	MessageID int64
	Complete  bool
	RetryAt   int64
}

// SavePushProgress commits one bounded scan, with no network work in the
// transaction. Crash before commit may replay accepted deliveries (at-least-once).
func (d *DB) SavePushProgress(ctx context.Context, progress []PushProgress, outcomes []PushOutcome) error {
	if len(progress) == 0 && len(outcomes) == 0 {
		return nil
	}
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixNano()
	for _, p := range progress {
		if p.Gone {
			if _, err = tx.ExecContext(ctx, "DELETE FROM push_subscriptions WHERE id=?", p.SubscriptionID); err != nil {
				return err
			}
			continue
		}
		// A subscription/message may have been deleted during the network call.
		_, err = tx.ExecContext(ctx, `INSERT INTO campfire_push_progress(message_id,subscription_id,completed,attempted_at)
		 SELECT ?,?,?,? WHERE EXISTS (SELECT 1 FROM campfire_push_outbox WHERE message_id=?) AND EXISTS (SELECT 1 FROM push_subscriptions WHERE id=?)
		 ON CONFLICT(message_id,subscription_id) DO UPDATE SET completed=excluded.completed,attempted_at=excluded.attempted_at`, p.MessageID, p.SubscriptionID, p.Completed, now, p.MessageID, p.SubscriptionID)
		if err != nil {
			return err
		}
	}
	for _, o := range outcomes {
		if o.Complete {
			_, err = tx.ExecContext(ctx, "DELETE FROM campfire_push_outbox WHERE message_id=?", o.MessageID)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE campfire_push_outbox SET retry_at=? WHERE message_id=?", o.RetryAt, o.MessageID)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) PushRoom(ctx context.Context, id int64) (Room, error) {
	r := Room{ID: id}
	err := d.Read.QueryRowContext(ctx, "SELECT coalesce(name,''),type FROM rooms WHERE id=?", id).Scan(&r.Name, &r.Type)
	return r, err
}
