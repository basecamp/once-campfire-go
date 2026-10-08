package fastdb

// SidebarMember is one row of the joined sidebar scan (ENGINE-20): the room's
// membership columns (from the viewer's own membership) plus one other
// member's user columns. Rows for non-direct rooms carry a zero Member — the
// LEFT JOIN only fetches the members of Rooms::Direct, which is the only room
// type the sidebar template renders members for — and the viewer is never
// included, mirroring the per-room displayRoom exclusion.
type SidebarMember struct {
	Room   SidebarRoom
	Member User
}

// querySidebarMembers replaces the sidebar's per-direct-room RoomMembers round
// trips with one statement: every sidebar room for the user in SidebarRooms
// order, each direct room expanded to its other members in the same order
// database.DB.RoomMembers returns them and the web displayRoom assembles them.
// The ORDER BY reproduces the per-room readers' plans exactly: SidebarRooms
// scans memberships through the (user_id) index, so equal lower(r.name) ties
// (direct rooms all sort as ”) fall out in the viewer's membership rowid
// order; RoomMembers scans the covering (room_id, user_id) index, so members
// fall out in user id order.
const querySidebarMembers = "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,r.updated_at,coalesce(viewer.involvement,''),viewer.unread_at IS NOT NULL," + userColumns + " FROM rooms r JOIN memberships viewer ON viewer.room_id=r.id AND viewer.user_id=? LEFT JOIN memberships m ON m.room_id=r.id AND m.user_id!=? AND r.type='Rooms::Direct' LEFT JOIN users u ON u.id=m.user_id WHERE viewer.involvement!='invisible' ORDER BY lower(r.name),viewer.rowid,u.id"

// SidebarMembers mirrors database.DB.SidebarRooms plus each direct room's
// members in one statement, appending to dst (pass dst[:0] to reuse a buffer).
// Like the database SidebarRooms reader it uses a nil accumulator, so an
// empty result stays nil. Grouping is the caller's job: the rows arrive in
// room order, each room contiguous; a non-direct room (or a direct room with
// no other members) contributes exactly one row with a zero Member.
func (c *Conn) SidebarMembers(dst []SidebarMember, user int64) ([]SidebarMember, error) {
	h, err := c.acquire(querySidebarMembers)
	if err != nil {
		return nil, err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return nil, err
	}
	if err := h.st.BindInt64(1, user); err != nil {
		return nil, err
	}
	if err := h.st.BindInt64(2, user); err != nil {
		return nil, err
	}
	rows := Rows{stmt: h.st}
	out := dst
	for rows.Next() {
		var row SidebarMember
		var err error
		if row.Room.ID, err = rows.Int64(0); err != nil {
			return nil, err
		}
		if row.Room.CreatorID, err = rows.Int64(1); err != nil {
			return nil, err
		}
		if row.Room.Name, err = rows.Text(2); err != nil {
			return nil, err
		}
		if row.Room.Type, err = rows.Text(3); err != nil {
			return nil, err
		}
		if row.Room.UpdatedAt, err = rows.Stamp(4); err != nil {
			return nil, err
		}
		if row.Room.Involvement, err = rows.Text(5); err != nil {
			return nil, err
		}
		row.Room.Unread = rows.Bool(6)
		if !rows.IsNull(7) {
			if row.Member, err = scanUserAt(&rows, 7); err != nil {
				return nil, err
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}
