package database

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

type Membership struct {
	ID, RoomID, UserID    int64
	Involvement           string
	UnreadAt, ConnectedAt *time.Time
	Connections           int
	UpdatedAt             time.Time
}

func (r Room) ParamKey() string {
	switch r.Type {
	case "Rooms::Closed":
		return "rooms_closed"
	case "Rooms::Direct":
		return "rooms_direct"
	default:
		return "rooms_open"
	}
}
func (r Room) DOM(prefix string) string {
	if prefix != "" {
		prefix += "_"
	}
	return fmt.Sprintf("%s%s_%d", prefix, r.ParamKey(), r.ID)
}
func (r Room) EditPath() string {
	return fmt.Sprintf("/rooms/%ss/%d/edit", strings.TrimPrefix(r.ParamKey(), "rooms_"), r.ID)
}
func (r Room) Noun() string {
	if r.Type == "Rooms::Direct" {
		return "Ping"
	}
	return "room"
}
func uniqueIDs(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}
func grant(ctx context.Context, tx *Tx, room, user int64, involvement, now string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO memberships(room_id,user_id,involvement,created_at,updated_at) SELECT ?,id,?,?,? FROM users WHERE id=? ON CONFLICT(room_id,user_id) DO NOTHING", room, involvement, now, now, user)
	return err
}
func (d *DB) CreateRoom(ctx context.Context, creator int64, kind, name string, users []int64) (Room, error) {
	var room Room
	if kind != "Rooms::Open" && kind != "Rooms::Closed" && kind != "Rooms::Direct" {
		return room, ErrValidation
	}
	if kind == "Rooms::Direct" {
		users = append(users, creator)
	}
	users = uniqueIDs(users)
	err := d.Transaction(ctx, func(tx *Tx) error {
		now := Stamp(d.Now())
		if kind == "Rooms::Direct" {
			rows, err := tx.QueryContext(ctx, "SELECT r.id,m.user_id FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE r.type='Rooms::Direct' ORDER BY r.id,m.user_id")
			if err != nil {
				return err
			}
			groups := map[int64][]int64{}
			var order []int64
			for rows.Next() {
				var id, user int64
				if err = rows.Scan(&id, &user); err != nil {
					rows.Close()
					return err
				}
				if _, ok := groups[id]; !ok {
					order = append(order, id)
				}
				groups[id] = append(groups[id], user)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, id := range order {
				if slices.Equal(groups[id], users) {
					room.ID = id
					return nil
				}
			}
		}
		var storedName any = name
		if kind == "Rooms::Direct" {
			storedName = nil
		}
		r, err := tx.ExecContext(ctx, "INSERT INTO rooms(name,type,creator_id,created_at,updated_at) VALUES (?,?,?,?,?)", storedName, kind, creator, now, now)
		if err != nil {
			return err
		}
		room.ID, err = r.LastInsertId()
		if err != nil {
			return err
		}
		if kind == "Rooms::Open" {
			_, err = tx.ExecContext(ctx, "INSERT INTO memberships(room_id,user_id,created_at,updated_at) SELECT ?,id,?,? FROM users WHERE status=0", room.ID, now, now)
			if err != nil {
				return err
			}
			return grant(ctx, tx, room.ID, creator, "mentions", now)
		}
		involvement := "mentions"
		if kind == "Rooms::Direct" {
			involvement = "everything"
		}
		for _, user := range users {
			if err := grant(ctx, tx, room.ID, user, involvement, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return room, err
	}
	err = d.Read.QueryRowContext(ctx, "SELECT id,creator_id,coalesce(name,''),type,updated_at FROM rooms WHERE id=?", room.ID).Scan(&room.ID, &room.CreatorID, &room.Name, &room.Type, timestamp{&room.UpdatedAt})
	return room, err
}
func (d *DB) UpdateRoom(ctx context.Context, id int64, kind, name string, users []int64) error {
	var revoked []int64
	err := d.Transaction(ctx, func(tx *Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, "SELECT type FROM rooms WHERE id=?", id).Scan(&old); err != nil {
			return err
		}
		if old == "Rooms::Direct" || kind != "Rooms::Open" && kind != "Rooms::Closed" {
			return ErrForbidden
		}
		now := Stamp(d.Now())
		if _, err := tx.ExecContext(ctx, "UPDATE rooms SET type=?,name=?,updated_at=? WHERE id=?", kind, name, now, id); err != nil {
			return err
		}
		if kind == "Rooms::Open" && old != kind {
			_, err := tx.ExecContext(ctx, "INSERT INTO memberships(room_id,user_id,created_at,updated_at) SELECT ?,id,?,? FROM users WHERE status=0 ON CONFLICT(room_id,user_id) DO NOTHING", id, now, now)
			return err
		}
		if kind == "Rooms::Closed" {
			rows, err := tx.QueryContext(ctx, "SELECT user_id FROM memberships WHERE room_id=?", id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var user int64
				if err := rows.Scan(&user); err != nil {
					rows.Close()
					return err
				}
				if !slices.Contains(users, user) {
					revoked = append(revoked, user)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}

			if len(users) == 0 {
				_, err := tx.ExecContext(ctx, "DELETE FROM memberships WHERE room_id=?", id)
				return err
			}
			args := []any{id}
			marks := make([]string, len(users))
			for i, user := range users {
				marks[i] = "?"
				args = append(args, user)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM memberships WHERE room_id=? AND user_id NOT IN ("+strings.Join(marks, ",")+")", args...); err != nil {
				return err
			}
			for _, user := range uniqueIDs(users) {
				if err := grant(ctx, tx, id, user, "mentions", now); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil && d.ResetConnections != nil {
		for _, user := range revoked {
			d.ResetConnections(user)
		}
	}
	return err
}
func (d *DB) DeleteRoom(ctx context.Context, id int64) error {
	var blobs []int64
	err := d.Transaction(ctx, func(tx *Tx) error {
		var err error
		blobs, err = AttachmentBlobIDs(ctx, tx, "(record_type='Message' AND record_id IN (SELECT id FROM messages WHERE room_id=?)) OR (record_type='ActionText::RichText' AND record_id IN (SELECT id FROM action_text_rich_texts WHERE record_type='Message' AND record_id IN (SELECT id FROM messages WHERE room_id=?)))", id, id)
		if err != nil {
			return err
		}
		for _, q := range []string{
			"DELETE FROM boosts WHERE message_id IN (SELECT id FROM messages WHERE room_id=?)",
			"DELETE FROM message_search_index WHERE rowid IN (SELECT id FROM messages WHERE room_id=?)",
			"DELETE FROM active_storage_attachments WHERE record_type='Message' AND record_id IN (SELECT id FROM messages WHERE room_id=?)",
			"DELETE FROM active_storage_attachments WHERE record_type='ActionText::RichText' AND record_id IN (SELECT id FROM action_text_rich_texts WHERE record_type='Message' AND record_id IN (SELECT id FROM messages WHERE room_id=?))",
			"DELETE FROM action_text_rich_texts WHERE record_type='Message' AND record_id IN (SELECT id FROM messages WHERE room_id=?)",
			"DELETE FROM messages WHERE room_id=?", "DELETE FROM memberships WHERE room_id=?", "DELETE FROM rooms WHERE id=?",
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		d.PurgeDetached(blobs)
	}
	return err
}
func (d *DB) Involvement(ctx context.Context, user, room int64) (string, error) {
	var value string
	err := d.Read.QueryRowContext(ctx, "SELECT involvement FROM memberships WHERE user_id=? AND room_id=?", user, room).Scan(&value)
	return value, err
}
func (d *DB) SetInvolvement(ctx context.Context, user, room int64, value string) error {
	if !slices.Contains([]string{"invisible", "nothing", "mentions", "everything"}, value) {
		return ErrValidation
	}
	r, err := d.Write.ExecContext(ctx, "UPDATE memberships SET involvement=?,updated_at=? WHERE user_id=? AND room_id=?", value, Stamp(d.Now()), user, room)
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
	return nil
}
func (d *DB) Presence(ctx context.Context, user, room int64, action string) error {
	return d.Transaction(ctx, func(tx *Tx) error {
		now := d.Now()
		stamp, cutoff := Stamp(now), Stamp(now.Add(-60*time.Second))
		var query string
		switch action {
		case "present":
			query = "UPDATE memberships SET connections=CASE WHEN connected_at>=? THEN connections+1 ELSE 1 END,connected_at=?,unread_at=NULL WHERE user_id=? AND room_id=?"
		case "refresh":
			query = "UPDATE memberships SET connections=CASE WHEN connected_at>=? THEN connections ELSE 1 END,connected_at=? WHERE user_id=? AND room_id=?"
		case "absent":
			_, err := tx.ExecContext(ctx, "UPDATE memberships SET connections=CASE WHEN connected_at>=? THEN max(0,connections-1) ELSE 0 END WHERE user_id=? AND room_id=?", cutoff, user, room)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "UPDATE memberships SET connected_at=NULL WHERE user_id=? AND room_id=? AND connections<1", user, room)
			return err
		default:
			return ErrValidation
		}
		_, err := tx.ExecContext(ctx, query, cutoff, stamp, user, room)
		return err
	})
}

// OriginalRoom follows Room.original (creation order, not the fixture ID order).
func (d *DB) OriginalRoom(ctx context.Context, user int64) (int64, error) {
	var id int64
	err := d.Read.QueryRowContext(ctx, "SELECT rooms.id FROM rooms JOIN memberships ON memberships.room_id=rooms.id WHERE memberships.user_id=? ORDER BY rooms.created_at LIMIT 1", user).Scan(&id)
	return id, err
}

// SidebarRoom loads membership state alongside the room, avoiding per-room queries.
type SidebarRoom struct {
	Room
	Involvement string
	Unread      bool
}

func (d *DB) SidebarRooms(ctx context.Context, user int64) ([]SidebarRoom, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at,coalesce(m.involvement,''),m.unread_at IS NOT NULL FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE m.user_id=? AND m.involvement!='invisible' ORDER BY lower(r.name)", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rooms []SidebarRoom
	for rows.Next() {
		var r SidebarRoom
		if err := rows.Scan(&r.ID, &r.CreatorID, &r.Name, &r.Type, timestamp{&r.UpdatedAt}, &r.Involvement, &r.Unread); err != nil {
			return nil, err
		}
		rooms = append(rooms, r)
	}
	return rooms, rows.Err()
}
