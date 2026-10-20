package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// UserDisplay contains presentation dependencies, never credentials or authority.
type UserDisplay struct {
	ID        int64
	Name, Bio string
	UpdatedAt time.Time
}

func (u UserDisplay) Title() string {
	parts := []string{}
	for _, value := range []string{u.Name, u.Bio} {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " – ")
}

type MessageDisplay struct {
	Message      Message
	Author       *UserDisplay
	Room         Room
	Participants []RoomParticipant
	Boosts       []Boost
	Attachment   *Blob
}

// MessageDisplays materializes associations in a short read snapshot. The caller
// supplies owned records (including commit receipts); no rendering or IO holds the
// connection. Each relationship is loaded separately to avoid multiplied rows.
// Authors belong to their message; the separate user map contains only mentions.
func (d *DB) MessageDisplays(ctx context.Context, records []Message, mentioned []int64) (map[int64]MessageDisplay, map[int64]UserDisplay, error) {
	result := make(map[int64]MessageDisplay, len(records))
	users := make(map[int64]UserDisplay)
	if len(records) == 0 {
		return result, users, nil
	}
	tx, err := d.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	result, users, err = messageDisplays(ctx, tx, records, mentioned)
	if err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	return result, users, nil
}

func messageDisplays(ctx context.Context, tx *sql.Tx, records []Message, mentioned []int64) (map[int64]MessageDisplay, map[int64]UserDisplay, error) {
	result := make(map[int64]MessageDisplay, len(records))
	users := make(map[int64]UserDisplay)
	if len(records) == 0 {
		return result, users, nil
	}
	roomIDs := map[int64]bool{}
	userIDs := map[int64]bool{}
	for _, m := range records {
		roomIDs[m.RoomID] = true
		userIDs[m.CreatorID] = true
	}
	for _, id := range mentioned {
		userIDs[id] = true
	}
	rooms, participants, err := messageRooms(ctx, tx, roomIDs)
	if err != nil {
		return nil, nil, err
	}
	users, err = userDisplays(ctx, tx, userIDs)
	if err != nil {
		return nil, nil, err
	}
	valid := map[int64]bool{}
	for _, m := range records {
		room, ok := rooms[m.RoomID]
		if !ok {
			return nil, nil, sql.ErrNoRows
		}
		data := MessageDisplay{Message: m, Room: room, Participants: participants[room.ID]}
		if author, ok := users[m.CreatorID]; ok {
			data.Author = &author
			valid[m.ID] = true
		}
		result[m.ID] = data
	}
	if len(valid) > 0 {
		boosts, err := messageBoosts(ctx, tx, valid)
		if err != nil {
			return nil, nil, err
		}
		attachments, err := messageAttachments(ctx, tx, valid)
		if err != nil {
			return nil, nil, err
		}
		for id := range valid {
			data := result[id]
			data.Boosts = boosts[id]
			data.Attachment = attachments[id]
			result[id] = data
		}
	}
	mentions := make(map[int64]UserDisplay, len(mentioned))
	for _, id := range mentioned {
		if user, exists := users[id]; exists {
			mentions[id] = user
		}
	}
	return result, mentions, nil
}

func displayIDs(ids map[int64]bool) string {
	list := make([]int64, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	raw, _ := json.Marshal(list) // int64 slices cannot fail JSON encoding.
	return string(raw)
}

func messageRooms(ctx context.Context, tx *sql.Tx, ids map[int64]bool) (map[int64]Room, map[int64][]RoomParticipant, error) {
	rooms := make(map[int64]Room, len(ids))
	participants := make(map[int64][]RoomParticipant)
	rows, err := tx.QueryContext(ctx, "SELECT id,creator_id,coalesce(name,''),type,updated_at FROM rooms WHERE id IN (SELECT value FROM json_each(?))", displayIDs(ids))
	if err != nil {
		return nil, nil, err
	}
	directs := map[int64]bool{}
	for rows.Next() {
		var r Room
		if err = rows.Scan(&r.ID, &r.CreatorID, &r.Name, &r.Type, timestamp{&r.UpdatedAt}); err != nil {
			rows.Close()
			return nil, nil, err
		}
		rooms[r.ID] = r
		if r.Type == "Rooms::Direct" {
			directs[r.ID] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if len(directs) == 0 {
		return rooms, participants, nil
	}
	rows, err = tx.QueryContext(ctx, "SELECT m.room_id,u.id,u.name,u.updated_at FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.room_id IN (SELECT value FROM json_each(?)) ORDER BY m.room_id,m.user_id", displayIDs(directs))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var room int64
		var u RoomParticipant
		if err = rows.Scan(&room, &u.ID, &u.Name, timestamp{&u.UpdatedAt}); err != nil {
			return nil, nil, err
		}
		participants[room] = append(participants[room], u)
	}
	return rooms, participants, rows.Err()
}

func messageBoosts(ctx context.Context, tx *sql.Tx, ids map[int64]bool) (map[int64][]Boost, error) {
	rows, err := tx.QueryContext(ctx, "SELECT b.id,b.message_id,b.booster_id,b.content,u.name,coalesce(u.bio,''),u.updated_at,b.created_at,b.updated_at FROM boosts b JOIN users u ON u.id=b.booster_id WHERE b.message_id IN (SELECT value FROM json_each(?)) ORDER BY b.created_at", displayIDs(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64][]Boost, len(ids))
	for rows.Next() {
		var b Boost
		var bio string
		if err = rows.Scan(&b.ID, &b.MessageID, &b.BoosterID, &b.Content, &b.Booster, &bio, timestamp{&b.BoosterUpdatedAt}, timestamp{&b.CreatedAt}, timestamp{&b.UpdatedAt}); err != nil {
			return nil, err
		}
		b.BoosterTitle = (UserDisplay{Name: b.Booster, Bio: bio}).Title()
		result[b.MessageID] = append(result[b.MessageID], b)
	}
	return result, rows.Err()
}

func messageAttachments(ctx context.Context, tx *sql.Tx, ids map[int64]bool) (map[int64]*Blob, error) {
	rows, err := tx.QueryContext(ctx, "SELECT a.record_id,"+blobColumns+" FROM active_storage_attachments a JOIN active_storage_blobs b ON b.id=a.blob_id WHERE a.record_type='Message' AND a.name='attachment' AND a.record_id IN (SELECT value FROM json_each(?)) AND a.id=(SELECT a2.id FROM active_storage_attachments a2 JOIN active_storage_blobs b2 ON b2.id=a2.blob_id WHERE a2.record_type='Message' AND a2.name='attachment' AND a2.record_id=a.record_id ORDER BY a2.id LIMIT 1)", displayIDs(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]*Blob, len(ids))
	for rows.Next() {
		var id int64
		var b Blob
		var metadata, checksum sql.NullString
		if err = rows.Scan(&id, &b.ID, &b.Key, &b.Filename, &b.ContentType, &metadata, &b.ServiceName, &b.ByteSize, &checksum, &b.CreatedAt); err != nil {
			return nil, err
		}
		b.Metadata = json.RawMessage(metadata.String)
		if !json.Valid(b.Metadata) {
			b.Metadata = json.RawMessage("{}")
		}
		b.Checksum = checksum.String
		result[id] = &b
	}
	return result, rows.Err()
}
