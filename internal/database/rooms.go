package database

import (
	"context"
	"fmt"
	"slices"
	"time"
)

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
