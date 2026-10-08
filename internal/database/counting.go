package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// Statement counting for whole-path assertions (ENGINE-45): a driver wrapper
// that counts every statement the pools execute — queries and executeds on
// the read pool, the write pool, transactions and the writer's jobs alike,
// because database/sql funnels them all through the connection.
//
// Drivers are registered once per process, so one counter backs every
// OpenCounting call; callers reset it between seeding and the measured
// request. The assertion tests open one counted database at a time.
type statementCounter struct {
	n atomic.Int64
	// recorded keeps the statement texts seen since the last reset, for
	// assertions and debugging; guarded by mu.
	mu       sync.Mutex
	recorded []string
}

func (c *statementCounter) reset() {
	c.n.Store(0)
	c.mu.Lock()
	c.recorded = nil
	c.mu.Unlock()
}
func (c *statementCounter) count() int64 { return c.n.Load() }
func (c *statementCounter) record(query string) {
	c.n.Add(1)
	c.mu.Lock()
	if len(c.recorded) < 4096 {
		c.recorded = append(c.recorded, query)
	}
	c.mu.Unlock()
}
func (c *statementCounter) dump() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.recorded...)
}

var countedStatements statementCounter

type countedDriver struct{}

func (countedDriver) Open(dsn string) (driver.Conn, error) {
	inner, err := (&sqlite3.SQLiteDriver{}).Open(dsn)
	if err != nil {
		return nil, err
	}
	return &countedConn{SQLiteConn: inner.(*sqlite3.SQLiteConn)}, nil
}

// countedConn embeds the concrete sqlite connection, so every optional
// database/sql interface (ConnBeginTx, ConnPrepareContext, Pinger, …) is
// satisfied by promotion; only the statement entry points are overridden to
// count. Prepared statements are wrapped too, because the read pool replays
// cached statements whose executions never reach the connection methods.
type countedConn struct {
	*sqlite3.SQLiteConn
}

func (c *countedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	countedStatements.record(query)
	return c.SQLiteConn.ExecContext(ctx, query, args)
}

func (c *countedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	countedStatements.record(query)
	return c.SQLiteConn.QueryContext(ctx, query, args)
}

// countedStmt embeds the driver statement; the counted methods shadow the
// promoted ones for executions, while Close/NumInput and the rest forward.
// ExecContext and QueryContext are optional driver interfaces, so the
// fallback paths convert to positional values for the driver's Exec/Query.
type countedStmt struct {
	driver.Stmt
}

func (s *countedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	countedStatements.n.Add(1)
	if ex, ok := s.Stmt.(driver.StmtExecContext); ok {
		return ex.ExecContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for i, a := range args {
		values[i] = a.Value
	}
	return s.Stmt.Exec(values)
}

func (s *countedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	countedStatements.n.Add(1)
	if q, ok := s.Stmt.(driver.StmtQueryContext); ok {
		return q.QueryContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for i, a := range args {
		values[i] = a.Value
	}
	return s.Stmt.Query(values)
}

func (c *countedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.SQLiteConn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &countedStmt{Stmt: stmt}, nil
}

func init() {
	sql.Register("sqlite3-counted", countedDriver{})
}

// OpenCounting opens a database like Open with every statement the read
// pool, the write pool and the writer's transactions execute counted into a
// counter (the checkpointer's connection is counted too, so assertions
// should pin CAMPFIRE_CHECKPOINT_MS to keep its ticks out of the measured
// request). The counter is process-global: exactly one counted database
// should be open during a measurement. The returned functions report and
// reset the count and dump the recorded statements; reset between seeding
// and the request under test.
func OpenCounting(path string, readers int) (*DB, func() int64, func(), func() []string, error) {
	db, err := open("sqlite3-counted", path, readers)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	countedStatements.reset()
	return db, countedStatements.count, countedStatements.reset, countedStatements.dump, nil
}
