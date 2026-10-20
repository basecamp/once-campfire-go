package web

import (
	"context"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Read persisted bodies through the real scoped observation rather than keeping
// obsolete production convenience methods for fixture assertions.
func messageRecords(db *database.DB, ctx context.Context, room, anchor int64) ([]database.Message, error) {
	owner, err := db.FindRoom(ctx, room)
	if err != nil {
		return nil, err
	}
	read, err := db.BeginMessageRead(ctx)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	refs, err := read.PageReferences(ctx, owner.CreatorID, room, anchor, "before")
	if err != nil {
		return nil, err
	}
	records, err := read.Records(ctx, refs)
	if err != nil {
		return nil, err
	}
	return records, read.Finish()
}
