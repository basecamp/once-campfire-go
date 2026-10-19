package database

import (
	"context"
	"strings"
	"time"
)

type PushSubscription struct {
	ID, UserID                     int64
	Endpoint, Key, Auth, UserAgent string
	RetryAt                        int64 // earliest retry Unix second; zero for untouched recipients
}

const pushColumns = "p.id,p.user_id,coalesce(p.endpoint,''),coalesce(p.p256dh_key,''),coalesce(p.auth_key,''),coalesce(p.user_agent,'')"

func pushRow(row interface{ Scan(...any) error }) (p PushSubscription, err error) {
	err = row.Scan(&p.ID, &p.UserID, &p.Endpoint, &p.Key, &p.Auth, &p.UserAgent)
	return
}
func (d *DB) PushSubscription(ctx context.Context, user, id int64) (PushSubscription, error) {
	return pushRow(d.Read.QueryRowContext(ctx, "SELECT "+pushColumns+" FROM push_subscriptions p WHERE p.user_id=? AND p.id=?", user, id))
}
func (d *DB) PushSubscriptions(ctx context.Context, user int64) ([]PushSubscription, error) {
	return d.pushQuery(ctx, "SELECT "+pushColumns+" FROM push_subscriptions p WHERE p.user_id=?", user)
}
func (d *DB) pushQuery(ctx context.Context, query string, args ...any) ([]PushSubscription, error) {
	rows, err := d.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []PushSubscription{}
	for rows.Next() {
		p, err := pushRow(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}
func (d *DB) FindPushSubscription(ctx context.Context, user int64, attrs map[string]*string) (PushSubscription, error) {
	query := "SELECT " + pushColumns + " FROM push_subscriptions p WHERE p.user_id=?"
	args := []any{user}
	for _, key := range []string{"endpoint", "p256dh_key", "auth_key"} {
		if value, ok := attrs[key]; ok {
			query += " AND p." + key + " IS ?"
			args = append(args, value)
		}
	}
	return pushRow(d.Read.QueryRowContext(ctx, query+" LIMIT 1", args...))
}
func (d *DB) SavePushSubscription(ctx context.Context, user int64, attrs map[string]*string, agent string) error {
	now := Stamp(d.Now())
	_, err := d.Write.ExecContext(ctx, "INSERT INTO push_subscriptions(user_id,endpoint,p256dh_key,auth_key,user_agent,created_at,updated_at) VALUES(?,?,?,?,?,?,?)", user, attrs["endpoint"], attrs["p256dh_key"], attrs["auth_key"], agent, now, now)
	return err
}
func (d *DB) TouchPushSubscription(ctx context.Context, id int64) error {
	_, err := d.Write.ExecContext(ctx, "UPDATE push_subscriptions SET updated_at=? WHERE id=?", Stamp(d.Now()), id)
	return err
}
func (d *DB) DeletePushSubscription(ctx context.Context, user, id int64) error {
	_, err := d.Write.ExecContext(ctx, "DELETE FROM push_subscriptions WHERE user_id=? AND id=?", user, id)
	return err
}
func (d *DB) UnreadCount(ctx context.Context, user int64) (int64, error) {
	var count int64
	err := d.Read.QueryRowContext(ctx, "SELECT count(*) FROM memberships WHERE user_id=? AND unread_at IS NOT NULL", user).Scan(&count)
	return count, err
}
func (d *DB) PushRecipients(ctx context.Context, room, creator int64, mentioned []int64) ([]PushSubscription, error) {
	return d.pushRecipients(ctx, 0, room, creator, mentioned, 0)
}

// PendingPushRecipients rechecks current membership, presence and subscription
// state on every replay. Acknowledged recipients are excluded; never-attempted
// recipients precede failed ones. Sleeping failures remain in the result with
// their RetryAt, so an empty due set cannot be mistaken for completed delivery.
// The first sleeping candidate bounds the next retry even beyond the page limit.
func (d *DB) PendingPushRecipients(ctx context.Context, message, room, creator int64, mentioned []int64, limit int) ([]PushSubscription, error) {
	return d.pushRecipients(ctx, message, room, creator, mentioned, limit)
}

// CurrentPushRecipient refreshes keys and eligibility immediately before sending.
// Membership changes after this read can still race the network side effect.
func (d *DB) CurrentPushRecipient(ctx context.Context, id, user, room, creator int64, mentioned bool) (PushSubscription, error) {
	const query = "SELECT " + pushColumns + " FROM push_subscriptions p JOIN users u ON u.id=p.user_id JOIN memberships m ON m.user_id=u.id WHERE p.id=? AND p.user_id=? AND m.room_id=? AND m.user_id!=? AND (m.connected_at IS NULL OR m.connected_at<?) AND (m.involvement='everything' OR (m.involvement='mentions' AND ?))"
	return pushRow(d.Read.QueryRowContext(ctx, query, id, user, room, creator, Stamp(d.Now().Add(-time.Minute)), mentioned))
}

func (d *DB) pushRecipients(ctx context.Context, message, room, creator int64, mentioned []int64, limit int) ([]PushSubscription, error) {
	query := "SELECT " + pushColumns
	args := make([]any, 0, len(mentioned)+6)
	if message != 0 {
		query += ",coalesce((SELECT attempted_at FROM campfire_push_progress g WHERE g.message_id=? AND g.subscription_id=p.id),0) AS attempted_at"
		args = append(args, message)
	}
	query += " FROM push_subscriptions p JOIN users u ON u.id=p.user_id JOIN memberships m ON m.user_id=u.id WHERE m.room_id=? AND m.user_id!=? AND (m.connected_at IS NULL OR m.connected_at<?) AND (m.involvement='everything'"
	args = append(args, room, creator, Stamp(d.Now().Add(-time.Minute)))
	if len(mentioned) > 0 {
		query += " OR (m.involvement='mentions' AND m.user_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(mentioned)), ",") + "))"
		for _, id := range mentioned {
			args = append(args, id)
		}
	}
	query += ")"
	if message != 0 {
		query += " AND NOT EXISTS (SELECT 1 FROM campfire_push_progress g WHERE g.message_id=? AND g.subscription_id=p.id AND g.completed=1) ORDER BY attempted_at,p.id LIMIT ?"
		args = append(args, message, limit)
		rows, err := d.Read.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		list := make([]PushSubscription, 0, limit)
		for rows.Next() {
			var p PushSubscription
			var attempted int64
			if err := rows.Scan(&p.ID, &p.UserID, &p.Endpoint, &p.Key, &p.Auth, &p.UserAgent, &attempted); err != nil {
				return nil, err
			}
			if attempted != 0 {
				// Round up, since the scheduler stores whole Unix seconds.
				p.RetryAt = time.Unix(0, attempted).Add(30*time.Second).Unix() + 1
			}
			list = append(list, p)
		}
		return list, rows.Err()
	} else {
		query += " ORDER BY CASE m.involvement WHEN 'everything' THEN 0 ELSE 1 END,p.id"
	}
	return d.pushQuery(ctx, query, args...)
}
func (d *DB) RoomMemberIDs(ctx context.Context, room int64) ([]int64, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT user_id FROM memberships WHERE room_id=?", room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
