package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

type RoomCommit struct {
	Room     Room
	Revoked  []int64
	Detached []int64
}

func (d *DB) CreateRoom(
	ctx context.Context,
	creator int64,
	kind string,
	name *sql.NullString,
	users []int64,
) (Room, error) {
	var room Room
	if kind != "Rooms::Open" && kind != "Rooms::Closed" && kind != "Rooms::Direct" {
		return room, ErrValidation
	}
	users = uniqueIDs(users)
	if kind == "Rooms::Direct" {
		users = uniqueIDs(append(users, creator))
	}
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		if err := mayCreateRoom(ctx, tx, creator, kind); err != nil {
			return err
		}
		createdAt := d.Now().UTC()
		now := Stamp(createdAt)
		if kind == "Rooms::Direct" {
			selected, err := json.Marshal(users)
			if err != nil {
				return err
			}
			// Rails selects existing users before comparing direct-room membership.
			rows, err := tx.QueryContext(
				ctx,
				"SELECT id FROM users WHERE id IN (SELECT value FROM json_each(?)) ORDER BY id",
				string(selected),
			)
			if err != nil {
				return err
			}
			users = users[:0]
			for rows.Next() {
				var id int64
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				users = append(users, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			// Compare the exact set in SQLite instead of retaining every ping's users.
			err = tx.QueryRowContext(ctx, `SELECT r.id FROM rooms r JOIN memberships m ON m.room_id=r.id
				WHERE r.type='Rooms::Direct' AND r.id IN (SELECT room_id FROM memberships WHERE user_id=?) GROUP BY r.id
				HAVING count(*)=? AND sum(m.user_id IN (SELECT value FROM json_each(?)))=?
				ORDER BY r.id LIMIT 1`, creator, len(users), string(selected), len(users)).
				Scan(&room.ID)
			if err == nil {
				return commandRoom(ctx, tx, room.ID, &room)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		var storedName any = name
		if kind == "Rooms::Direct" {
			storedName = nil
		}
		r, err := tx.ExecContext(
			ctx,
			"INSERT INTO rooms(name,type,creator_id,created_at,updated_at) VALUES (?,?,?,?,?)",
			storedName,
			kind,
			creator,
			now,
			now,
		)
		if err != nil {
			return err
		}
		room.ID, err = r.LastInsertId()
		if err != nil {
			return err
		}
		room.CreatorID, room.Type, room.UpdatedAt = creator, kind, createdAt
		if name != nil && name.Valid && kind != "Rooms::Direct" {
			room.Name = name.String
		}
		if kind == "Rooms::Open" {
			_, err = tx.ExecContext(
				ctx,
				"INSERT INTO memberships(room_id,user_id,created_at,updated_at) SELECT ?,id,?,? FROM users WHERE status=0",
				room.ID,
				now,
				now,
			)
			if err != nil {
				return err
			}
			return nil
		}
		involvement := "mentions"
		if kind == "Rooms::Direct" {
			involvement = "everything"
		}
		return grantUsers(ctx, tx, room.ID, users, involvement, now)
	})
	if err != nil {
		return Room{}, err
	}
	return room, nil
}

func mayCreateRoom(ctx context.Context, tx *sql.Tx, actor int64, kind string) error {
	var role int
	if err := tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id=? AND status=0", actor).Scan(&role); err != nil {
		return err
	}
	if kind == "Rooms::Direct" || role == 1 {
		return nil
	}
	var settings string
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(settings,'{}') FROM accounts ORDER BY id LIMIT 1").Scan(&settings); err != nil {
		return err
	}
	if (Account{Settings: json.RawMessage(settings)}).RestrictRooms() {
		return ErrForbidden
	}
	return nil
}

func roomActor(ctx context.Context, tx *sql.Tx, actor, id int64) (Room, int, error) {
	var room Room
	var role int
	err := tx.QueryRowContext(ctx, "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at,u.role FROM rooms r JOIN memberships m ON m.room_id=r.id JOIN users u ON u.id=m.user_id WHERE r.id=? AND u.id=? AND u.status=0", id, actor).Scan(&room.ID, &room.CreatorID, &room.Name, &room.Type, timestamp{&room.UpdatedAt}, &role)
	return room, role, err
}

func grantUsers(ctx context.Context, tx *sql.Tx, room int64, users []int64, involvement, now string) error {
	if len(users) == 0 {
		return nil
	}
	selected, err := json.Marshal(users)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO memberships(room_id,user_id,involvement,created_at,updated_at) SELECT ?,id,?,?,? FROM users WHERE id IN (SELECT value FROM json_each(?)) ON CONFLICT(room_id,user_id) DO NOTHING", room, involvement, now, now, string(selected))
	return err
}

func (d *DB) UpdateRoom(ctx context.Context, actor, id int64, kind string, name *sql.NullString, users []int64) (RoomCommit, error) {
	users = uniqueIDs(users)
	selected, err := json.Marshal(users)
	if err != nil {
		return RoomCommit{}, err
	}
	var result RoomCommit
	err = d.Transaction(ctx, func(tx *sql.Tx) error {
		room, role, err := roomActor(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if room.Type == "Rooms::Direct" || kind == "Rooms::Direct" {
			return sql.ErrNoRows
		}
		if role != 1 && room.CreatorID != actor {
			return ErrForbidden
		}
		if kind != "Rooms::Open" && kind != "Rooms::Closed" {
			return ErrValidation
		}
		result.Room = room
		var oldName sql.NullString
		if err = tx.QueryRowContext(ctx, "SELECT name FROM rooms WHERE id=?", id).Scan(&oldName); err != nil {
			return err
		}
		nextName := oldName
		if name != nil {
			nextName = *name
		}
		now := d.Now().UTC()
		stamp := Stamp(now)
		if kind != room.Type || nextName != oldName {
			if _, err = tx.ExecContext(ctx, "UPDATE rooms SET type=?,name=?,updated_at=? WHERE id=?", kind, nextName, stamp, id); err != nil {
				return err
			}
			result.Room.Type, result.Room.UpdatedAt = kind, now
			result.Room.Name = ""
			if nextName.Valid {
				result.Room.Name = nextName.String
			}
		}
		if kind == "Rooms::Open" && room.Type != kind {
			_, err = tx.ExecContext(ctx, "INSERT INTO memberships(room_id,user_id,created_at,updated_at) SELECT ?,id,?,? FROM users WHERE status=0 ON CONFLICT(room_id,user_id) DO NOTHING", id, stamp, stamp)
			return err
		}
		if kind == "Rooms::Closed" {
			retained := make(map[int64]bool, len(users))
			for _, user := range users {
				retained[user] = true
			}
			rows, err := tx.QueryContext(ctx, "SELECT user_id FROM memberships WHERE room_id=?", id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var user int64
				if err = rows.Scan(&user); err != nil {
					rows.Close()
					return err
				}
				if !retained[user] {
					result.Revoked = append(result.Revoked, user)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM memberships WHERE room_id=? AND user_id NOT IN (SELECT value FROM json_each(?))", id, string(selected)); err != nil {
				return err
			}
			return grantUsers(ctx, tx, id, users, "mentions", stamp)
		}
		return nil
	})
	if err != nil {
		return RoomCommit{}, err
	}
	return result, nil
}

func (d *DB) DeleteRoom(ctx context.Context, actor, id int64) (RoomCommit, error) {
	return d.deleteRoom(ctx, actor, id, false)
}

// DeleteDirectRoom preserves the participant-authorized direct namespace.
func (d *DB) DeleteDirectRoom(ctx context.Context, actor, id int64) (RoomCommit, error) {
	return d.deleteRoom(ctx, actor, id, true)
}

func (d *DB) deleteRoom(ctx context.Context, actor, id int64, direct bool) (RoomCommit, error) {
	var result RoomCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		room, role, err := roomActor(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if direct {
			if room.Type != "Rooms::Direct" {
				return sql.ErrNoRows
			}
		} else if role != 1 && room.CreatorID != actor {
			return ErrForbidden
		}
		result.Room = room
		result.Detached, err = AttachmentBlobIDs(ctx, tx, "(record_type='Message' AND record_id IN (SELECT id FROM messages WHERE room_id=?)) OR (record_type='ActionText::RichText' AND record_id IN (SELECT id FROM action_text_rich_texts WHERE record_type='Message' AND record_id IN (SELECT id FROM messages WHERE room_id=?)))", id, id)
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
			if _, err = tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return RoomCommit{}, err
	}
	return result, nil
}

type InvolvementCommit struct {
	RoomInvolvement
	Previous string
}

// ChangeInvolvement selects current authority, room and previous value under the
// same writer transaction as the update. Sidebar effects consume this receipt,
// not an earlier HTTP read or a mandatory postcommit reconstruction.
func (d *DB) ChangeInvolvement(ctx context.Context, user, room int64, value string) (InvolvementCommit, error) {
	var commit InvolvementCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		state, err := scanRoomInvolvement(tx.QueryRowContext(ctx, roomInvolvementSelect+
			" AND EXISTS (SELECT 1 FROM users WHERE id=? AND status=0)", user, room, user))
		if err != nil {
			return err
		}
		var stored any
		if strings.TrimSpace(value) != "" {
			if !slices.Contains([]string{"invisible", "nothing", "mentions", "everything"}, value) {
				return ErrValidation
			}
			stored = value
		} else {
			value = ""
		}
		if _, err = tx.ExecContext(ctx, "UPDATE memberships SET involvement=?,updated_at=? WHERE user_id=? AND room_id=?",
			stored, Stamp(d.Now()), user, room); err != nil {
			return err
		}
		commit = InvolvementCommit{RoomInvolvement: state, Previous: state.Value}
		commit.Value = value
		return nil
	})
	if err != nil {
		return InvolvementCommit{}, err
	}
	return commit, nil
}

// commandRoom captures metadata before the transaction releases its observation.
func commandRoom(ctx context.Context, tx *sql.Tx, id int64, room *Room) error {
	return tx.QueryRowContext(ctx, "SELECT id,creator_id,coalesce(name,''),type,updated_at FROM rooms WHERE id=?", id).Scan(&room.ID, &room.CreatorID, &room.Name, &room.Type, timestamp{&room.UpdatedAt})
}
