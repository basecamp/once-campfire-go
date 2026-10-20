package database

import (
	"context"
	"encoding/json"
	"fmt"
)

// BotDisplay includes the API credential deliberately displayed to administrators,
// but not password digests, email addresses or unrelated authorization state.
type BotDisplay struct {
	UserDisplay
	BotToken string
}

func (b BotDisplay) BotKey() string { return fmt.Sprintf("%d-%s", b.ID, b.BotToken) }

type BotIdentity struct {
	ID           int64
	Name         string
	Role, Status int
}

type SharedRoom struct {
	ID   int64
	Name string
}

func (d *DB) BotDisplays(ctx context.Context) ([]BotDisplay, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT id,name,coalesce(bio,''),updated_at,coalesce(bot_token,'') FROM users WHERE status=0 AND role=2 ORDER BY lower(name)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bots []BotDisplay
	for rows.Next() {
		var bot BotDisplay
		if err := rows.Scan(&bot.ID, &bot.Name, &bot.Bio, timestamp{&bot.UpdatedAt}, &bot.BotToken); err != nil {
			return nil, err
		}
		bots = append(bots, bot)
	}
	return bots, rows.Err()
}

// BotSharedRooms prepares the retained bot set without rechecking admission or
// excluding invisible/nullable memberships. Direct rooms have no API instructions.
func (d *DB) BotSharedRooms(ctx context.Context, bots []int64) (map[int64][]SharedRoom, error) {
	rooms := make(map[int64][]SharedRoom)
	if len(bots) == 0 {
		return rooms, nil
	}
	ids, err := json.Marshal(bots)
	if err != nil {
		return nil, err
	}
	rows, err := d.Read.QueryContext(ctx, `SELECT m.user_id,r.id,coalesce(r.name,'')
FROM memberships m JOIN rooms r ON r.id=m.room_id
WHERE m.user_id IN (SELECT value FROM json_each(?)) AND r.type!='Rooms::Direct'
ORDER BY m.user_id,lower(r.name)`, string(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var user int64
		var room SharedRoom
		if err := rows.Scan(&user, &room.ID, &room.Name); err != nil {
			return nil, err
		}
		rooms[user] = append(rooms[user], room)
	}
	return rooms, rows.Err()
}

func (d *DB) BotIdentity(ctx context.Context, id int64) (BotIdentity, error) {
	var bot BotIdentity
	err := d.Read.QueryRowContext(ctx, "SELECT id,name,role,status FROM users WHERE id=?", id).Scan(&bot.ID, &bot.Name, &bot.Role, &bot.Status)
	return bot, err
}

func (d *DB) BotWebhook(ctx context.Context, id int64) (string, error) {
	var url string
	err := d.Read.QueryRowContext(ctx, "SELECT url FROM webhooks WHERE user_id=?", id).Scan(&url)
	return url, err
}
