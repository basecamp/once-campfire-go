package database

import "context"

func (d *DB) AutocompleteUsers(ctx context.Context, room int64, query string) ([]UserDisplay, error) {
	sql := "SELECT u.id,u.name,coalesce(u.bio,''),u.updated_at FROM users u "
	args := []any{}
	if room != 0 {
		sql += "JOIN memberships m ON m.user_id=u.id AND m.room_id=? "
		args = append(args, room)
	}
	sql += "WHERE u.status=0"
	if query != "" {
		sql += " AND u.name LIKE ?"
		args = append(args, "%"+query+"%")
	}
	sql += " ORDER BY lower(u.name)"
	rows, err := d.Read.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return displayRows(rows)
}
