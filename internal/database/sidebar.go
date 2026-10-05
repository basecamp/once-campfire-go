package database

import (
	"context"
	"encoding/json"
	"slices"
)

// Sidebar reads the current room list, direct-room members, and placeholders in
// one SQLite snapshot. User summaries contain only the sidebar's display fields.
func (d *DB) Sidebar(ctx context.Context, user int64) ([]SidebarRoom, map[int64][]User, []User, error) {
	data, err := snapshotRows(d, ctx, sidebarSQL, decodeSidebar, user, user, user)
	if err != nil {
		return nil, nil, nil, err
	}
	members := make(map[int64][]User)
	for _, member := range data[0].members {
		members[member.room] = append(members[member.room], member.user)
	}
	return slices.Clone(data[0].rooms), members, slices.Clone(data[0].placeholders), nil
}

const sidebarSQL = `WITH my_rooms AS MATERIALIZED (
	SELECT r.id,r.creator_id,coalesce(r.name,'') AS name,r.type,r.updated_at,
		coalesce(m.involvement,'') AS involvement,m.unread_at IS NOT NULL AS unread
	FROM rooms r JOIN memberships m ON m.room_id=r.id
	WHERE m.user_id=? AND m.involvement!='invisible' ORDER BY lower(r.name)
), direct_users AS (
	SELECT DISTINCT user_id FROM memberships WHERE room_id IN (
		SELECT r.id FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE r.type='Rooms::Direct' AND m.user_id=?
	)
), excluded AS (SELECT user_id AS id FROM direct_users UNION ALL SELECT ?)
SELECT json_array(json_object(
	'Rooms',(SELECT json_group_array(json_object('ID',id,'CreatorID',creator_id,'Name',name,'Type',type,
		'UpdatedAt',updated_at,'Involvement',involvement,'Unread',unread)) FROM my_rooms),
	'Members',(SELECT json_group_array(json_object('Room',room_id,'ID',id,'Name',name,'UpdatedAt',updated_at)) FROM (
		SELECT m.room_id,u.id,u.name,u.updated_at FROM memberships m JOIN users u ON u.id=m.user_id
		WHERE m.room_id IN (SELECT id FROM my_rooms WHERE type='Rooms::Direct') ORDER BY m.room_id,m.user_id
	)),
	'Placeholders',(SELECT json_group_array(json_object('ID',id,'Name',name,'UpdatedAt',updated_at)) FROM (
		SELECT u.id,u.name,u.updated_at FROM users u WHERE u.status=0 AND u.id NOT IN (SELECT id FROM excluded)
		ORDER BY u.created_at ASC LIMIT max(0,20-(SELECT count(*) FROM excluded))
	))
))`

type sidebarMember struct {
	room int64
	user User
}
type sidebarData struct {
	rooms        []SidebarRoom
	members      []sidebarMember
	placeholders []User
}

func decodeSidebar(raw string) ([]sidebarData, error) {
	var values []struct {
		Rooms   []snapshotSidebarRoom
		Members []struct {
			Room int64
			snapshotUser
		}
		Placeholders []snapshotUser
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	result := make([]sidebarData, len(values))
	for i, value := range values {
		for _, v := range value.Rooms {
			r := v.SidebarRoom
			r.Unread = v.Unread != 0
			if err := (timestamp{&r.UpdatedAt}).Scan(v.UpdatedAt); err != nil {
				return nil, err
			}
			result[i].rooms = append(result[i].rooms, r)
		}
		for _, v := range value.Members {
			u := v.User
			if err := (timestamp{&u.UpdatedAt}).Scan(v.UpdatedAt); err != nil {
				return nil, err
			}
			result[i].members = append(result[i].members, sidebarMember{v.Room, u})
		}
		for _, v := range value.Placeholders {
			u := v.User
			if err := (timestamp{&u.UpdatedAt}).Scan(v.UpdatedAt); err != nil {
				return nil, err
			}
			result[i].placeholders = append(result[i].placeholders, u)
		}
	}
	return result, nil
}
