package fastdb

import (
	"fmt"
	"strings"
)

// queryDirectMembers is the first pass of internal/database/accounts.go's
// DirectPlaceholders: every user sharing a direct room with the caller,
// including the caller themself (they are a member of their own direct
// rooms), de-duplicated.
const queryDirectMembers = "SELECT DISTINCT user_id FROM memberships WHERE room_id IN (SELECT r.id FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE r.type='Rooms::Direct' AND m.user_id=?)"

// DirectPlaceholders mirrors database.DB.DirectPlaceholders
// (internal/database/accounts.go:299) exactly: the ids of everyone the
// caller shares a direct room with, the caller appended again even when
// already listed (duplicates are intentional — accounts.go appends without
// checking), and then every active user not in that list, ordered by
// creation, limited to fill the sidebar's 20-slot placeholder list. The LIMIT
// is inlined into the query text exactly like accounts.go, and the NOT IN
// marks grow with the id count.
//
// Like the database reader it appends to dst and returns a non-nil empty
// slice when nothing matches.
//
// The two statements are held in separate variables on purpose: stmtRef
// releases through a pointer receiver, so `defer h.release()` after
// reassigning h would resolve both deferred calls against the same variable
// slot and release the second statement twice (leaving the first un-reset).
func (c *Conn) DirectPlaceholders(dst []User, user int64) ([]User, error) {
	first, err := c.acquire(queryDirectMembers)
	if err != nil {
		return nil, err
	}
	defer first.release()
	if err := first.st.ClearBindings(); err != nil {
		return nil, err
	}
	if err := first.st.BindInt64(1, user); err != nil {
		return nil, err
	}
	rows := Rows{stmt: first.st}
	ids := make([]int64, 0, 16)
	for rows.Next() {
		id, err := rows.Int64(0)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ids = append(ids, user)
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	query := fmt.Sprintf("SELECT "+userColumns+" FROM users u WHERE u.status=0 AND u.id NOT IN (%s) ORDER BY u.created_at ASC LIMIT %d", marks, max(0, 20-len(ids)))
	second, err := c.acquire(query)
	if err != nil {
		return nil, err
	}
	defer second.release()
	if err := second.st.ClearBindings(); err != nil {
		return nil, err
	}
	for i, id := range ids {
		if err := second.st.BindInt64(i+1, id); err != nil {
			return nil, err
		}
	}
	rows = Rows{stmt: second.st}
	out := dst
	if out == nil {
		out = []User{}
	}
	for rows.Next() {
		member, err := scanUser(&rows)
		if err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}
