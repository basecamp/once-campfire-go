package database

import (
	"context"
	"encoding/json"
)

// WebhookTarget is loaded by an already-admitted job. Role and membership are
// deliberately not rechecked here: mentioned bots may be outside the room, and
// queued trusted replies retain their original admission policy.
type WebhookTarget struct {
	ID                    int64
	Name, Token, Endpoint string
}

func (d *DB) WebhookTarget(ctx context.Context, bot int64) (WebhookTarget, error) {
	var target WebhookTarget
	err := d.Read.QueryRowContext(ctx, `SELECT u.id,u.name,coalesce(u.bot_token,''),coalesce(w.url,'')
 FROM users u JOIN webhooks w ON w.user_id=u.id WHERE u.id=? LIMIT 1`, bot).
		Scan(&target.ID, &target.Name, &target.Token, &target.Endpoint)
	return target, err
}

// WebhookMessage retains nullable, untransformed HTML alongside its author and
// room. This is an unscoped worker read, not a bot HTTP authorization query.
type WebhookMessage struct {
	ID, RoomID, CreatorID int64
	Creator, RoomName     string
	HTML                  *string
}

func (d *DB) WebhookMessage(ctx context.Context, id int64) (WebhookMessage, error) {
	var message WebhookMessage
	err := d.Read.QueryRowContext(ctx, `SELECT m.id,m.room_id,m.creator_id,coalesce(u.name,''),coalesce(r.name,''),t.body
 FROM messages m JOIN rooms r ON r.id=m.room_id
 LEFT JOIN users u ON u.id=m.creator_id
 LEFT JOIN action_text_rich_texts t ON t.record_type='Message' AND t.record_id=m.id AND t.name='body'
 WHERE m.id=? LIMIT 1`, id).Scan(&message.ID, &message.RoomID, &message.CreatorID, &message.Creator, &message.RoomName, &message.HTML)
	return message, err
}

func (d *DB) DirectWebhookRecipients(ctx context.Context, room, creator int64) ([]int64, error) {
	return d.webhookRecipients(ctx, `SELECT u.id FROM users u JOIN memberships m ON m.user_id=u.id
 WHERE m.room_id=? AND u.status=0 AND u.role=2 AND u.id!=? ORDER BY lower(u.name)`, room, creator)
}

func (d *DB) MentionedWebhookRecipients(ctx context.Context, mentioned []int64, creator int64) ([]int64, error) {
	if len(mentioned) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(mentioned)
	if err != nil {
		return nil, err
	}
	// Keep mention order without a per-ID full-user load. There is intentionally
	// no membership predicate for a verified mentioned bot.
	return d.webhookRecipients(ctx, `SELECT u.id FROM json_each(?) selected JOIN users u ON u.id=selected.value
 WHERE u.status=0 AND u.role=2 AND u.id!=? GROUP BY u.id ORDER BY min(selected.key)`, string(raw), creator)
}

func (d *DB) webhookRecipients(ctx context.Context, query string, args ...any) ([]int64, error) {
	rows, err := d.Read.QueryContext(ctx, query, args...)
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

// UnreadCounts materializes badges before notification admission. Missing groups
// mean zero unread memberships, just as the single-user count does.
func (d *DB) UnreadCounts(ctx context.Context, users []int64) (map[int64]int64, error) {
	counts := make(map[int64]int64, len(users))
	if len(users) == 0 {
		return counts, nil
	}
	raw, err := json.Marshal(uniqueIDs(users))
	if err != nil {
		return nil, err
	}
	rows, err := d.Read.QueryContext(ctx, `SELECT user_id,count(*) FROM memberships
 WHERE user_id IN (SELECT value FROM json_each(?)) AND unread_at IS NOT NULL GROUP BY user_id`, string(raw))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, count int64
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		counts[id] = count
	}
	return counts, rows.Err()
}
