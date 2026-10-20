package database

import "context"

// Test fixtures read the database-owned selection without production convenience
// wrappers. Scoped authorization is covered by MessageRead and API observations.
func pageReferences(d *DB, ctx context.Context, room, anchor int64, direction string) ([]MessageReference, error) {
	read, err := d.BeginMessageRead(ctx)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	refs, err := messagePageReferences(ctx, read.tx, room, anchor, direction)
	if err != nil {
		return nil, err
	}
	return refs, read.Finish()
}

func messageRecords(d *DB, ctx context.Context, room, anchor int64, direction string) ([]Message, error) {
	read, err := d.BeginMessageRead(ctx)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	refs, err := messagePageReferences(ctx, read.tx, room, anchor, direction)
	if err != nil {
		return nil, err
	}
	records, err := messageReferenceRecords(ctx, read.tx, refs)
	if err != nil {
		return nil, err
	}
	return records, read.Finish()
}
