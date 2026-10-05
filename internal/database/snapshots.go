package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sync"
)

// Persistent observer connections check data_version before every lookup. Versions
// are compared only against the same connection's previous observation. A change
// advances the shared cache generation, including commits by external writers.
// Authentication and single-room authorization continue to query current rows.
type snapshotObserver struct {
	conn       *sql.Conn
	version    *sql.Stmt
	generation int64
}

type snapshotCache struct {
	mu         sync.Mutex
	db         *sql.DB
	observers  []*snapshotObserver
	available  chan *snapshotObserver
	generation int64
	rows       map[string]any
}

func openSnapshots(uri string, readers int) (*snapshotCache, error) {
	c := &snapshotCache{available: make(chan *snapshotObserver, readers)}
	db, err := sql.Open("sqlite3", uri)
	if err != nil {
		return nil, err
	}
	c.db = db
	db.SetMaxOpenConns(readers)
	db.SetMaxIdleConns(readers)
	ctx := context.Background()
	for range readers {
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, errors.Join(err, c.close())
		}
		statement, err := conn.PrepareContext(ctx, "PRAGMA data_version")
		if err != nil {
			return nil, errors.Join(err, conn.Close(), c.close())
		}
		observer := &snapshotObserver{conn: conn, version: statement}
		c.observers = append(c.observers, observer)
		if err := statement.QueryRowContext(ctx).Scan(&observer.generation); err != nil {
			return nil, errors.Join(err, c.close())
		}
		c.available <- observer
	}
	return c, nil
}

func (c *snapshotCache) close() error {
	var err error
	for _, observer := range c.observers {
		err = errors.Join(err, observer.version.Close(), observer.conn.Close())
	}
	return errors.Join(err, c.db.Close())
}

func (c *snapshotCache) current(ctx context.Context) (int64, error) {
	var observer *snapshotObserver
	select {
	case observer = <-c.available:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	defer func() { c.available <- observer }()
	var version int64
	if err := observer.version.QueryRowContext(ctx).Scan(&version); err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if version != observer.generation {
		clear(c.rows)
		c.generation++
		observer.generation = version
	}
	return c.generation, nil
}

func snapshotRows[T any](d *DB, ctx context.Context, query string, decode func(string) ([]T, error), args ...any) ([]T, error) {
	parameters, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	key := query + "\x00" + string(parameters)
	version, err := d.snapshots.current(ctx)
	if err != nil {
		return nil, err
	}
	d.snapshots.mu.Lock()
	rows, ok := d.snapshots.rows[key].([]T)
	d.snapshots.mu.Unlock()
	if ok {
		return slices.Clone(rows), nil
	}
	var raw string
	if err := d.Read.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		return nil, err
	}
	rows, err = decode(raw)
	if err != nil {
		return nil, err
	}
	// Check again after the query: a concurrent commit must not install an old
	// snapshot under the new version. Cache at most 128 arrays of <=64 KiB.
	if len(raw) <= 64<<10 && len(parameters) <= 4096 {
		current, err := d.snapshots.current(ctx)
		d.snapshots.mu.Lock()
		if err == nil && current == version && d.snapshots.generation == version {
			if len(d.snapshots.rows) >= 128 {
				clear(d.snapshots.rows)
			}
			if d.snapshots.rows == nil {
				d.snapshots.rows = make(map[string]any)
			}
			d.snapshots.rows[key] = slices.Clone(rows)
		}
		d.snapshots.mu.Unlock()
	}
	return rows, nil
}

type snapshotUser struct {
	User
	UpdatedAt string
}

const userJSON = `json_object('ID',u.id,'Name',coalesce(u.name,''),'Email',coalesce(u.email_address,''),'Password',coalesce(u.password_digest,''),'Role',u.role,'Status',u.status,'Bio',coalesce(u.bio,''),'UpdatedAt',u.updated_at,'BotToken',coalesce(u.bot_token,''))`

func decodeUsers(raw string) ([]User, error) {
	var values []snapshotUser
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	users := make([]User, len(values))
	for i, value := range values {
		users[i] = value.User
		if err := (timestamp{&users[i].UpdatedAt}).Scan(value.UpdatedAt); err != nil {
			return nil, err
		}
	}
	return users, nil
}

type snapshotMessage struct {
	Message
	CreatedAt, UpdatedAt string
}

func decodeMessages(raw string) ([]Message, error) {
	var values []snapshotMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	messages := make([]Message, len(values))
	for i, value := range values {
		messages[i] = value.Message
		if value.CreatedAt != "" {
			if err := (timestamp{&messages[i].CreatedAt}).Scan(value.CreatedAt); err != nil {
				return nil, err
			}
		}
		if err := (timestamp{&messages[i].UpdatedAt}).Scan(value.UpdatedAt); err != nil {
			return nil, err
		}
	}
	return messages, nil
}

type snapshotSidebarRoom struct {
	SidebarRoom
	UpdatedAt string
	Unread    int
}

func decodeSidebarRooms(raw string) ([]SidebarRoom, error) {
	var values []snapshotSidebarRoom
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	rooms := make([]SidebarRoom, len(values))
	for i, value := range values {
		rooms[i] = value.SidebarRoom
		rooms[i].Unread = value.Unread != 0
		if err := (timestamp{&rooms[i].UpdatedAt}).Scan(value.UpdatedAt); err != nil {
			return nil, err
		}
	}
	return rooms, nil
}

type snapshotRoom struct {
	Room
	UpdatedAt string
}

func decodeRooms(raw string) ([]Room, error) {
	var values []snapshotRoom
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	rooms := make([]Room, len(values))
	for i, value := range values {
		rooms[i] = value.Room
		if err := (timestamp{&rooms[i].UpdatedAt}).Scan(value.UpdatedAt); err != nil {
			return nil, err
		}
	}
	return rooms, nil
}

func decodeAccounts(raw string) ([]Account, error) {
	var values []struct {
		Account
		UpdatedAt, Settings string
		HasLogo             int
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	accounts := make([]Account, len(values))
	for i, value := range values {
		accounts[i] = value.Account
		accounts[i].Settings = json.RawMessage(value.Settings)
		accounts[i].HasLogo = value.HasLogo != 0
		if err := (timestamp{&accounts[i].UpdatedAt}).Scan(value.UpdatedAt); err != nil {
			return nil, err
		}
	}
	return accounts, nil
}
