package database

import (
	"context"
	"database/sql/driver"

	"github.com/mattn/go-sqlite3"
)

// SQLite work runs to completion once it starts, as on the reference's reader and
// writer threads: a request whose client goes away still finishes its statement.
// With a cancellable context go-sqlite3 runs each Exec on a new goroutine and watches
// each query's context, and database/sql adds a watcher goroutine per transaction
// and result set. Stripping cancellation where the database is entered avoids those
// hand-offs, notably on the single writer, where every statement of every write
// waited for one. As with the reference's read queue, a read waits for a connection
// even if its caller stops waiting; waiting for the writer remains cancellable.
func uncancelled(ctx context.Context) context.Context {
	if ctx.Done() == nil {
		return ctx
	}
	return context.WithoutCancel(ctx)
}

type uncancelledDriver struct{ sqlite3.SQLiteDriver }

func (d *uncancelledDriver) Open(dsn string) (driver.Conn, error) {
	conn, err := d.SQLiteDriver.Open(dsn)
	if err != nil {
		return nil, err
	}
	return uncancelledConn{conn.(*sqlite3.SQLiteConn)}, nil
}

type uncancelledConn struct{ *sqlite3.SQLiteConn }

// sqliteConn is the driver connection from sql.Conn.Raw.
func sqliteConn(conn any) *sqlite3.SQLiteConn {
	if wrapped, ok := conn.(uncancelledConn); ok {
		return wrapped.SQLiteConn
	}
	return conn.(*sqlite3.SQLiteConn)
}

func (c uncancelledConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.SQLiteConn.BeginTx(uncancelled(ctx), opts)
}
func (c uncancelledConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.SQLiteConn.ExecContext(uncancelled(ctx), query, args)
}
func (c uncancelledConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.SQLiteConn.QueryContext(uncancelled(ctx), query, args)
}
func (c uncancelledConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	statement, err := c.SQLiteConn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return uncancelledStmt{statement.(*sqlite3.SQLiteStmt)}, nil
}

type uncancelledStmt struct{ *sqlite3.SQLiteStmt }

func (s uncancelledStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.SQLiteStmt.ExecContext(uncancelled(ctx), args)
}
func (s uncancelledStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.SQLiteStmt.QueryContext(uncancelled(ctx), args)
}
