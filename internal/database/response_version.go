package database

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

type versionResult struct {
	value uint64
	err   error
}

// PRAGMA data_version is connection-local. One worker owns its read-only
// connection and statement; no read transaction survives an observation.
// The bounded queue contains result channels, never request contexts.
type versionObserver struct {
	database   *sql.DB
	connection *sql.Conn
	statement  *sql.Stmt
	requests   chan chan versionResult
	admission  chan struct{}
	context    context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	closeOnce  sync.Once
	closeError error
}

func openVersionObserver(dsn string) (*versionObserver, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		return nil, err
	}
	stmt, err := conn.PrepareContext(context.Background(), "PRAGMA data_version")
	if err != nil {
		conn.Close()
		db.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	v := &versionObserver{
		database: db, connection: conn, statement: stmt,
		requests: make(chan chan versionResult, 256), admission: make(chan struct{}, 1),
		context: ctx, cancel: cancel, done: make(chan struct{}),
	}
	go v.observe()
	return v, nil
}

func (v *versionObserver) observe() {
	defer close(v.done)
	var batch [64]chan versionResult
	for {
		select {
		case <-v.context.Done():
			return
		case first := <-v.requests:
			batch[0] = first
		}
		n := 1
	collect:
		for n < len(batch) {
			select {
			case next := <-v.requests:
				batch[n] = next
				n++
			default:
				break collect
			}
		}
		// Every member requested its observation BEFORE this query starts.
		// Arrivals during Scan/Close belong to the NEXT query, never this batch:
		// sharing an in-flight read could hide a commit after that read began.
		var value uint64
		err := v.statement.QueryRowContext(v.context).Scan(&value)
		for i := range n {
			batch[i] <- versionResult{value, err}
			batch[i] = nil
		}
	}
}

func (v *versionObserver) Close() error {
	v.closeOnce.Do(func() {
		v.cancel()
		// Join admission too: a sender racing cancellation must finish before
		// the worker is joined and the retained result channels are released.
		v.admission <- struct{}{}
		<-v.admission
		<-v.done // join the active Scan/Close before closing its resources
		// Cancelled callers need not receive a result; release their bounded queue.
		for {
			select {
			case <-v.requests:
			default:
				v.closeError = errors.Join(v.statement.Close(), v.connection.Close(), v.database.Close())
				return
			}
		}
	})
	return v.closeError
}

func (v *versionObserver) request(ctx context.Context) (chan versionResult, error) {
	select {
	case v.admission <- struct{}{}:
		defer func() { <-v.admission }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-v.context.Done():
		return nil, v.context.Err()
	}
	if err := v.context.Err(); err != nil {
		return nil, err
	}
	result := make(chan versionResult, 1)
	select {
	case v.requests <- result:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-v.context.Done():
		return nil, v.context.Err()
	}
}

// Observe local and foreign commits afresh, without a TTL or local-only epoch.
// Cancellation stops this waiter, not the shared observation of other callers.
func (d *DB) ResponseVersion(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	v := d.version
	result, err := v.request(ctx)
	if err != nil {
		return 0, err
	}
	select {
	case observed := <-result:
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return observed.value, observed.err
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-v.context.Done():
		return 0, v.context.Err()
	}
}
