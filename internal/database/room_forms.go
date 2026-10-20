package database

import "context"

// RoomChoice owns everything rendered by the membership control, including its
// selection from the same observation as the active candidate list.
type RoomChoice struct {
	UserDisplay
	Selected bool
}

func (d *DB) RoomChoices(ctx context.Context, room, viewer int64) ([]RoomChoice, error) {
	rows, err := d.Read.QueryContext(ctx, `SELECT u.id,u.name,coalesce(u.bio,''),u.updated_at,
 CASE WHEN ?=0 THEN u.id=? ELSE EXISTS(SELECT 1 FROM memberships m WHERE m.room_id=? AND m.user_id=u.id) END
 FROM users u WHERE u.status=0 ORDER BY lower(u.name)`, room, viewer, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var choices []RoomChoice
	for rows.Next() {
		var choice RoomChoice
		if err := rows.Scan(&choice.ID, &choice.Name, &choice.Bio, timestamp{&choice.UpdatedAt}, &choice.Selected); err != nil {
			return nil, err
		}
		choices = append(choices, choice)
	}
	return choices, rows.Err()
}

// Direct settings retain inactive members. This is not an active-recipient read.
func (d *DB) RoomMemberDisplays(ctx context.Context, room int64) ([]UserDisplay, error) {
	rows, err := d.Read.QueryContext(ctx, `SELECT u.id,u.name,coalesce(u.bio,''),u.updated_at
 FROM users u JOIN memberships m ON m.user_id=u.id WHERE m.room_id=? ORDER BY m.user_id`, room)
	if err != nil {
		return nil, err
	}
	return displayRows(rows)
}
