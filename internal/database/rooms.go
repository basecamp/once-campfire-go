package database

import (
	"context"
	"database/sql"
	"encoding/json"
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

func (d *DB) RoomInvitation(ctx context.Context, room int64) (bool, error) {
	var invitation bool
	err := d.Read.QueryRowContext(ctx, "SELECT ?=(SELECT id FROM rooms ORDER BY created_at LIMIT 1) AND NOT EXISTS(SELECT 1 FROM messages WHERE room_id=? LIMIT 1 OFFSET 40)", room, room).Scan(&invitation)
	return invitation, err
}

// RoomInvolvement captures the room and membership setting in one observation.
type RoomInvolvement struct {
	Room
	Value string
}

const roomInvolvementSelect = "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at,coalesce(m.involvement,'') FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE m.user_id=? AND r.id=?"

func scanRoomInvolvement(row *sql.Row) (RoomInvolvement, error) {
	var state RoomInvolvement
	err := row.Scan(&state.ID, &state.CreatorID, &state.Name, &state.Type, timestamp{&state.UpdatedAt}, &state.Value)
	return state, err
}

func (d *DB) RoomInvolvement(ctx context.Context, user, room int64) (RoomInvolvement, error) {
	return scanRoomInvolvement(d.Read.QueryRowContext(ctx, roomInvolvementSelect, user, room))
}

func (d *DB) Presence(ctx context.Context, user, room int64, action string) error {
	return d.Transaction(ctx, func(tx *sql.Tx) error {
		now := d.Now()
		stamp, cutoff := Stamp(now), Stamp(now.Add(-60*time.Second))
		var query string
		switch action {
		case "present":
			query = "UPDATE memberships SET connections=CASE WHEN connected_at>=? THEN connections+1 ELSE 1 END,connected_at=?,unread_at=NULL WHERE user_id=? AND room_id=?"
		case "read":
			// An already-owned present refreshes presence and marks the room read
			// without adding another connection for the same subscription.
			query = "UPDATE memberships SET connections=CASE WHEN connected_at>=? THEN connections ELSE 1 END,connected_at=?,unread_at=NULL WHERE user_id=? AND room_id=?"
		case "refresh":
			query = "UPDATE memberships SET connections=CASE WHEN connected_at>=? THEN connections ELSE 1 END,connected_at=? WHERE user_id=? AND room_id=?"
		case "absent":
			_, err := tx.ExecContext(
				ctx,
				"UPDATE memberships SET connections=CASE WHEN connected_at>=? THEN max(0,connections-1) ELSE 0 END WHERE user_id=? AND room_id=?",
				cutoff,
				user,
				room,
			)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(
				ctx,
				"UPDATE memberships SET connected_at=NULL WHERE user_id=? AND room_id=? AND connections<1",
				user,
				room,
			)
			return err
		default:
			return ErrValidation
		}
		_, err := tx.ExecContext(ctx, query, cutoff, stamp, user, room)
		return err
	})
}

// NewestRoom preserves the index redirect's descending membership room ID,
// independently of the creation-order fallback used by room navigation.
func (d *DB) NewestRoom(ctx context.Context, user int64) (int64, error) {
	var id int64
	err := d.Read.QueryRowContext(ctx, "SELECT room_id FROM memberships WHERE user_id=? ORDER BY room_id DESC LIMIT 1", user).Scan(&id)
	return id, err
}

// OriginalRoom follows Room.original (creation order, not the fixture ID order).
func (d *DB) OriginalRoom(ctx context.Context, user int64) (int64, error) {
	var id int64
	err := d.Read.QueryRowContext(ctx, "SELECT rooms.id FROM rooms JOIN memberships ON memberships.room_id=rooms.id WHERE memberships.user_id=? ORDER BY rooms.created_at LIMIT 1", user).
		Scan(&id)
	return id, err
}

// RoomMembership owns current membership state and retained direct participants.
type RoomMembership struct {
	Room
	Involvement string
	Unread      bool
	Members     []RoomParticipant
}

func (d *DB) SidebarRooms(ctx context.Context, user int64) ([]RoomMembership, error) {
	return d.roomMemberships(ctx, user, true)
}

// ProfileRooms includes invisible and nullable-involvement memberships. Filtering
// them like the sidebar would hide notification settings the user can still edit.
func (d *DB) ProfileRooms(ctx context.Context, user int64) ([]RoomMembership, error) {
	return d.roomMemberships(ctx, user, false)
}

func (d *DB) roomMemberships(ctx context.Context, user int64, visible bool) ([]RoomMembership, error) {
	query := "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at,coalesce(m.involvement,''),m.unread_at IS NOT NULL FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE m.user_id=?"
	if visible {
		query += " AND m.involvement!='invisible'"
	}
	rows, err := d.Read.QueryContext(ctx, query+" ORDER BY lower(r.name)", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rooms []RoomMembership
	for rows.Next() {
		var r RoomMembership
		if err := rows.Scan(&r.ID, &r.CreatorID, &r.Name, &r.Type, timestamp{&r.UpdatedAt}, &r.Involvement, &r.Unread); err != nil {
			return nil, err
		}
		rooms = append(rooms, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	positions := make(map[int64]int)
	var directIDs []int64
	for i, room := range rooms {
		if room.Type == "Rooms::Direct" {
			positions[room.ID] = i
			directIDs = append(directIDs, room.ID)
		}
	}
	if len(directIDs) == 0 {
		return rooms, nil
	}
	selected, err := json.Marshal(directIDs)
	if err != nil {
		return nil, err
	}
	// Hydrate the retained room set, just as individual RoomMembers reads did.
	// Rechecking viewer visibility here could turn a concurrently hidden ping
	// into a self-ping. Room entry still performs its own fresh authorization.
	rows, err = d.Read.QueryContext(ctx, `
SELECT m.room_id,u.id,u.name,u.updated_at
FROM memberships m JOIN users u ON u.id=m.user_id
WHERE m.room_id IN (SELECT value FROM json_each(?))
ORDER BY m.room_id,m.user_id`, string(selected))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var room int64
		var participant RoomParticipant
		if err := rows.Scan(&room, &participant.ID, &participant.Name, timestamp{&participant.UpdatedAt}); err != nil {
			return nil, err
		}
		i := positions[room]
		rooms[i].Members = append(rooms[i].Members, participant)
	}
	return rooms, rows.Err()
}
