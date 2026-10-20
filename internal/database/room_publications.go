package database

import (
	"cmp"
	"context"
	"slices"
)

// RoomAudience separates active recipients from retained ping participants.
// Deactivated/banned users remain in a ping's display but receive no publication.
type RoomAudience struct {
	Active       []int64
	Participants []RoomParticipant
}

func (d *DB) RoomPublicationAudience(ctx context.Context, room Room) (RoomAudience, error) {
	var audience RoomAudience
	if room.Type != "Rooms::Direct" {
		rows, err := d.Read.QueryContext(ctx, "SELECT u.id FROM users u JOIN memberships m ON m.user_id=u.id WHERE m.room_id=? AND u.status=0 ORDER BY lower(u.name)", room.ID)
		if err != nil {
			return audience, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return RoomAudience{}, err
			}
			audience.Active = append(audience.Active, id)
		}
		return audience, rows.Err()
	}
	// One observation selects both sets. Recipient order remains name-based;
	// retained participant order remains ID-based for personalized ping names.
	rows, err := d.Read.QueryContext(ctx, "SELECT u.id,u.name,u.updated_at,u.status FROM users u JOIN memberships m ON m.user_id=u.id WHERE m.room_id=? ORDER BY lower(u.name)", room.ID)
	if err != nil {
		return audience, err
	}
	defer rows.Close()
	for rows.Next() {
		var member RoomParticipant
		var status int
		if err := rows.Scan(&member.ID, &member.Name, timestamp{&member.UpdatedAt}, &status); err != nil {
			return RoomAudience{}, err
		}
		audience.Participants = append(audience.Participants, member)
		if status == 0 {
			audience.Active = append(audience.Active, member.ID)
		}
	}
	if err := rows.Err(); err != nil {
		return RoomAudience{}, err
	}
	slices.SortFunc(audience.Participants, func(a, b RoomParticipant) int { return cmp.Compare(a.ID, b.ID) })
	return audience, nil
}
