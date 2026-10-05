package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"crawshaw.io/sqlite"
)

// ErrNoRows is returned by Row.Scan when the query selected no row.
var ErrNoRows = errors.New("sql: no rows in result set")

// Scanner is implemented by scan destinations that convert a column value themselves. The
// value is nil, int64, float64, string or []byte.
type Scanner interface{ Scan(value any) error }

// NullString is a TEXT column that may be NULL.
type NullString struct {
	String string
	Valid  bool
}

// statementCacheLimit bounds each connection's prepared statements, as the reference's
// STATEMENT_CACHE_CAPACITY does; queries past it are prepared for one use.
const statementCacheLimit = 256

// Statements run through SQLite's C API: prepared (and cached) per connection, bound by position
// and read by position, as the reference's rusqlite does. A connection is used by one goroutine
// at a time: a reader taken from the pool, or the writer goroutine's own.

func prepareStmt(conn *sqlite.Conn, query string, args []any) (stmt *sqlite.Stmt, transient bool, err error) {
	if conn.CachedStatements() < statementCacheLimit || conn.HasStatement(query) {
		stmt, err = conn.Prepare(query)
	} else {
		var trailing int
		stmt, trailing, err = conn.PrepareTransient(query)
		if err == nil && trailing != 0 {
			stmt.Finalize()
			err = fmt.Errorf("sqlite: %q has trailing bytes", query)
		}
		transient = true
	}
	if err != nil {
		return nil, false, err
	}
	if err = bind(stmt, args); err != nil {
		release(stmt, transient)
		return nil, false, err
	}
	return stmt, transient, nil
}

func release(stmt *sqlite.Stmt, transient bool) {
	if transient {
		stmt.Finalize()
	} else {
		stmt.Reset()
	}
}

func bind(stmt *sqlite.Stmt, args []any) error {
	if want := stmt.BindParamCount(); want != len(args) {
		return fmt.Errorf("sqlite: expected %d arguments, got %d", want, len(args))
	}
	for i, arg := range args {
		if err := bindValue(stmt, i+1, arg); err != nil {
			return err
		}
	}
	return nil
}

func bindValue(stmt *sqlite.Stmt, param int, arg any) error {
	switch v := arg.(type) {
	case nil:
		stmt.BindNull(param)
	case string:
		stmt.BindText(param, v)
	case int64:
		stmt.BindInt64(param, v)
	case int:
		stmt.BindInt64(param, int64(v))
	case bool:
		stmt.BindBool(param, v)
	case []byte:
		if v == nil {
			stmt.BindNull(param)
		} else {
			stmt.BindBlob(param, v)
		}
	case float64:
		stmt.BindFloat(param, v)
	case *string:
		if v == nil {
			stmt.BindNull(param)
		} else {
			stmt.BindText(param, *v)
		}
	case time.Time:
		// go-sqlite3's encoding of time.Time arguments.
		stmt.BindText(param, v.Format("2006-01-02 15:04:05.999999999-07:00"))
	default:
		return bindReflect(stmt, param, reflect.ValueOf(arg))
	}
	return nil
}

func bindReflect(stmt *sqlite.Stmt, param int, v reflect.Value) error {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			stmt.BindNull(param)
			return nil
		}
		return bindValue(stmt, param, v.Elem().Interface())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		stmt.BindInt64(param, v.Int())
	case reflect.Uint8, reflect.Uint16, reflect.Uint32:
		stmt.BindInt64(param, int64(v.Uint()))
	case reflect.String:
		stmt.BindText(param, v.String())
	case reflect.Bool:
		stmt.BindBool(param, v.Bool())
	case reflect.Float32, reflect.Float64:
		stmt.BindFloat(param, v.Float())
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.Uint8 {
			return fmt.Errorf("sqlite: unsupported argument type %s", v.Type())
		}
		if v.IsNil() {
			stmt.BindNull(param)
		} else {
			stmt.BindBlob(param, v.Bytes())
		}
	default:
		return fmt.Errorf("sqlite: unsupported argument type %s", v.Type())
	}
	return nil
}

// scanRow reads the current row's columns into dest, converting as database/sql does for the
// destination types this application uses.
func scanRow(stmt *sqlite.Stmt, dest []any) error {
	if n := stmt.ColumnCount(); n != len(dest) {
		return fmt.Errorf("sqlite: expected %d destination arguments in Scan, not %d", n, len(dest))
	}
	for col, d := range dest {
		if err := scanColumn(stmt, col, d); err != nil {
			return fmt.Errorf("sqlite: Scan error on column index %d, name %q: %w", col, stmt.ColumnName(col), err)
		}
	}
	return nil
}

var errNull = errors.New("converting NULL is unsupported")

func scanColumn(stmt *sqlite.Stmt, col int, dest any) error {
	switch d := dest.(type) {
	case *int64:
		if stmt.ColumnType(col) == sqlite.SQLITE_NULL {
			return errNull
		}
		*d = stmt.ColumnInt64(col)
	case *int:
		if stmt.ColumnType(col) == sqlite.SQLITE_NULL {
			return errNull
		}
		*d = int(stmt.ColumnInt64(col))
	case *string:
		switch stmt.ColumnType(col) {
		case sqlite.SQLITE_NULL:
			return errNull
		case sqlite.SQLITE_INTEGER:
			*d = strconv.FormatInt(stmt.ColumnInt64(col), 10)
		case sqlite.SQLITE_FLOAT:
			*d = strconv.FormatFloat(stmt.ColumnFloat(col), 'g', -1, 64)
		default:
			*d = stmt.ColumnText(col)
		}
	case *bool:
		switch stmt.ColumnType(col) {
		case sqlite.SQLITE_NULL:
			return errNull
		case sqlite.SQLITE_INTEGER, sqlite.SQLITE_FLOAT:
			*d = stmt.ColumnInt64(col) != 0
		default:
			b, err := strconv.ParseBool(stmt.ColumnText(col))
			if err != nil {
				return err
			}
			*d = b
		}
	case timestamp:
		if stmt.ColumnType(col) == sqlite.SQLITE_NULL {
			return fmt.Errorf("invalid timestamp type <nil>")
		}
		return d.parse(stmt.ColumnText(col))
	case *NullString:
		if stmt.ColumnType(col) == sqlite.SQLITE_NULL {
			*d = NullString{}
			return nil
		}
		var s string
		if err := scanColumn(stmt, col, &s); err != nil {
			return err
		}
		*d = NullString{String: s, Valid: true}
	case *float64:
		if stmt.ColumnType(col) == sqlite.SQLITE_NULL {
			return errNull
		}
		*d = stmt.ColumnFloat(col)
	case *[]byte:
		if stmt.ColumnType(col) == sqlite.SQLITE_NULL {
			*d = nil
			return nil
		}
		b := make([]byte, stmt.ColumnLen(col))
		stmt.ColumnBytes(col, b)
		*d = b
	case *json.RawMessage:
		if stmt.ColumnType(col) == sqlite.SQLITE_NULL {
			*d = nil
			return nil
		}
		b := make([]byte, stmt.ColumnLen(col))
		stmt.ColumnBytes(col, b)
		*d = b
	case *any:
		*d = columnValue(stmt, col)
	case Scanner:
		return d.Scan(columnValue(stmt, col))
	default:
		return scanReflect(stmt, col, reflect.ValueOf(dest))
	}
	return nil
}

func columnValue(stmt *sqlite.Stmt, col int) any {
	switch stmt.ColumnType(col) {
	case sqlite.SQLITE_INTEGER:
		return stmt.ColumnInt64(col)
	case sqlite.SQLITE_FLOAT:
		return stmt.ColumnFloat(col)
	case sqlite.SQLITE_TEXT:
		return stmt.ColumnText(col)
	case sqlite.SQLITE_BLOB:
		b := make([]byte, stmt.ColumnLen(col))
		stmt.ColumnBytes(col, b)
		return b
	default:
		return nil
	}
}

func scanReflect(stmt *sqlite.Stmt, col int, dest reflect.Value) error {
	if dest.Kind() != reflect.Pointer || dest.IsNil() {
		return fmt.Errorf("destination not a pointer")
	}
	target := dest.Elem()
	if target.Kind() == reflect.Pointer {
		if stmt.ColumnType(col) == sqlite.SQLITE_NULL {
			target.SetZero()
			return nil
		}
		value := reflect.New(target.Type().Elem())
		if err := scanColumn(stmt, col, value.Interface()); err != nil {
			return err
		}
		target.Set(value)
		return nil
	}
	if stmt.ColumnType(col) == sqlite.SQLITE_NULL {
		return errNull
	}
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		target.SetInt(stmt.ColumnInt64(col))
	case reflect.String:
		var s string
		if err := scanColumn(stmt, col, &s); err != nil {
			return err
		}
		target.SetString(s)
	default:
		return fmt.Errorf("unsupported Scan, storing into type %s", target.Type())
	}
	return nil
}

// Rows is the result of a query; Next steps through it and Close releases its connection.
type Rows struct {
	stmt      *sqlite.Stmt
	transient bool
	pool      *readPool // the connection goes back to it on Close; nil on the writer
	conn      *sqlite.Conn
	err       error
	closed    bool
}

func (r *Rows) Next() bool {
	if r.closed {
		return false
	}
	row, err := r.stmt.Step()
	if err != nil {
		r.err = err
	}
	if err != nil || !row {
		r.Close()
		return false
	}
	return true
}

func (r *Rows) Scan(dest ...any) error {
	if r.closed {
		return errors.New("sql: Rows are closed")
	}
	return scanRow(r.stmt, dest)
}

func (r *Rows) Err() error { return r.err }

func (r *Rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	release(r.stmt, r.transient)
	if r.pool != nil {
		r.pool.put(r.conn)
	}
	return nil
}

// Row is the result of QueryRow; Scan reads its first row and releases its connection.
type Row struct {
	rows *Rows
	err  error
}

func (r *Row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		if r.rows.err != nil {
			return r.rows.err
		}
		return ErrNoRows
	}
	return r.rows.Scan(dest...)
}

func (r *Row) Err() error { return r.err }

// Result reports the effects of Exec.
type Result struct{ lastInsertID, rowsAffected int64 }

func (r Result) LastInsertId() (int64, error) { return r.lastInsertID, nil }
func (r Result) RowsAffected() (int64, error) { return r.rowsAffected, nil }

func query(conn *sqlite.Conn, pool *readPool, sql string, args []any) (*Rows, error) {
	stmt, transient, err := prepareStmt(conn, sql, args)
	if err != nil {
		return nil, err
	}
	return &Rows{stmt: stmt, transient: transient, pool: pool, conn: conn}, nil
}

func exec(conn *sqlite.Conn, sql string, args []any) (Result, error) {
	stmt, transient, err := prepareStmt(conn, sql, args)
	if err != nil {
		if len(args) == 0 && strings.Contains(err.Error(), "trailing bytes") {
			return execScript(conn, sql)
		}
		return Result{}, err
	}
	defer release(stmt, transient)
	for {
		row, err := stmt.Step()
		if err != nil {
			return Result{}, err
		}
		if !row {
			break
		}
	}
	return Result{lastInsertID: conn.LastInsertRowID(), rowsAffected: int64(conn.Changes())}, nil
}

// execScript runs several statements without arguments, such as the schema.
func execScript(conn *sqlite.Conn, script string) (Result, error) {
	for strings.TrimSpace(script) != "" {
		stmt, trailing, err := conn.PrepareTransient(script)
		if err != nil {
			return Result{}, err
		}
		for {
			row, err := stmt.Step()
			if err != nil {
				stmt.Finalize()
				return Result{}, err
			}
			if !row {
				break
			}
		}
		stmt.Finalize()
		script = script[len(script)-trailing:]
	}
	return Result{lastInsertID: conn.LastInsertRowID(), rowsAffected: int64(conn.Changes())}, nil
}

// Tx is the writer connection inside a write (Transaction) or a single statement (Writer.Exec).
// Its queries run on the writer goroutine; Rows must be consumed before the write returns.
type Tx struct {
	conn        *sqlite.Conn
	afterCommit []func(*Tx) error
	committed   bool
}

func (tx *Tx) QueryContext(_ context.Context, sql string, args ...any) (*Rows, error) {
	return query(tx.conn, nil, sql, args)
}
func (tx *Tx) QueryRowContext(_ context.Context, sql string, args ...any) *Row {
	rows, err := query(tx.conn, nil, sql, args)
	return &Row{rows: rows, err: err}
}
func (tx *Tx) ExecContext(_ context.Context, sql string, args ...any) (Result, error) {
	return exec(tx.conn, sql, args)
}
func (tx *Tx) Exec(sql string, args ...any) (Result, error) { return exec(tx.conn, sql, args) }
func (tx *Tx) QueryRow(sql string, args ...any) *Row {
	return tx.QueryRowContext(context.Background(), sql, args...)
}

// AfterCommit queues database work to run on the writer once the transaction commits, in its own
// implicit transaction, as Active Record runs after_commit callbacks (the reference's
// Tx::after_commit). Outside a transaction it runs right away.
func (tx *Tx) AfterCommit(hook func(*Tx) error) {
	if tx.committed {
		if err := hook(tx); err != nil {
			logAfterCommit(err)
		}
		return
	}
	tx.afterCommit = append(tx.afterCommit, hook)
}

// readPool is the reader connections. A read takes a free connection on the calling goroutine,
// or waits for one in arrival order, and gives it back when its rows are closed.
type readPool struct {
	conns chan *sqlite.Conn
	all   []*sqlite.Conn
}

func (p *readPool) get(ctx context.Context) (*sqlite.Conn, error) {
	select {
	case conn := <-p.conns:
		return conn, nil
	default:
	}
	select {
	case conn := <-p.conns:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *readPool) put(conn *sqlite.Conn) { p.conns <- conn }

func (p *readPool) QueryContext(ctx context.Context, sql string, args ...any) (*Rows, error) {
	conn, err := p.get(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := query(conn, p, sql, args)
	if err != nil {
		p.put(conn)
		return nil, err
	}
	return rows, nil
}

func (p *readPool) QueryRowContext(ctx context.Context, sql string, args ...any) *Row {
	rows, err := p.QueryContext(ctx, sql, args...)
	return &Row{rows: rows, err: err}
}

func (p *readPool) QueryRow(sql string, args ...any) *Row {
	return p.QueryRowContext(context.Background(), sql, args...)
}

// With runs fn with one reader connection held for all of its queries.
func (p *readPool) With(ctx context.Context, fn func(*sqlite.Conn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := p.get(ctx)
	if err != nil {
		return err
	}
	defer p.put(conn)
	return fn(conn)
}

func (p *readPool) Close() error {
	var errs []error
	for range p.all {
		errs = append(errs, (<-p.conns).Close())
	}
	return errors.Join(errs...)
}
