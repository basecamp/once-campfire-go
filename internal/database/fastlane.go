package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/basecamp/once-campfire-go/internal/fastdb/write"
)

// The direct write lane (ENGINE-55): the message create transaction runs as
// prepared statements on one write.WriteConn — cached by text, positional
// binds, step/reset — instead of database/sql. The lane's concurrency model
// is the fastdb connection's: a transaction owns the connection between
// begin and commit/rollback, so the writer loop, in-line jobs and the queued
// after-commit transactions serialize exactly like the database/sql lane's
// single-connection pool. Statement failures surface the same csqlite
// sentinels (errors.Is against csqlite.ErrConstraint/ErrBusy and
// write.ErrNoRows = sql.ErrNoRows) with the same rollback shapes the
// database/sql lane has; only the error text formatting differs (the csqlite
// form), a deliberate difference documented in README.md.

// fastWriteRecord, when non-nil (tests), receives every statement text the
// direct lane executes — transaction control included — so the
// statement-count contract can be pinned on the direct path, the way
// countedStatements pins the database/sql path.
var fastWriteRecord func(query string)

type fastLaneConn struct {
	conn *write.WriteConn
}

func (c *fastLaneConn) begin(ctx context.Context) (laneTx, error) {
	tx, err := c.conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &fastLaneTx{tx: tx}, nil
}

func (c *fastLaneConn) close() error { return c.conn.Close() }

type fastLaneTx struct {
	tx *write.WriteTx
}

func (t *fastLaneTx) MembershipCount(ctx context.Context, room, user int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s, err := t.tx.Stmt(stmtMembershipCount)
	if err != nil {
		return 0, err
	}
	defer s.Reset()
	if err := s.BindInt64(1, room); err != nil {
		return 0, err
	}
	if err := s.BindInt64(2, user); err != nil {
		return 0, err
	}
	row, err := s.Step()
	if err != nil {
		return 0, err
	}
	if !row {
		return 0, write.ErrNoRows
	}
	return s.Int64(0)
}

func (t *fastLaneTx) CreatorName(ctx context.Context, user int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s, err := t.tx.Stmt(stmtCreatorName)
	if err != nil {
		return "", err
	}
	defer s.Reset()
	if err := s.BindInt64(1, user); err != nil {
		return "", err
	}
	row, err := s.Step()
	if err != nil {
		return "", err
	}
	if !row {
		return "", write.ErrNoRows
	}
	return s.Text(0)
}

func (t *fastLaneTx) InsertMessage(ctx context.Context, client string, user, room int64, stamp string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s, err := t.tx.Stmt(stmtInsertMessage)
	if err != nil {
		return 0, err
	}
	defer s.Reset()
	if err := s.BindText(1, client); err != nil {
		return 0, err
	}
	if err := s.BindInt64(2, user); err != nil {
		return 0, err
	}
	if err := s.BindInt64(3, room); err != nil {
		return 0, err
	}
	if err := s.BindText(4, stamp); err != nil {
		return 0, err
	}
	if err := s.BindText(5, stamp); err != nil {
		return 0, err
	}
	if row, err := s.Step(); err != nil {
		return 0, err
	} else if row {
		return 0, errors.New("fastdb write: INSERT returned a row")
	}
	return t.tx.LastInsertID(), nil
}

func (t *fastLaneTx) TouchRoom(ctx context.Context, room int64, stamp string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := t.tx.Stmt(stmtTouchRoom)
	if err != nil {
		return err
	}
	defer s.Reset()
	if err := s.BindText(1, stamp); err != nil {
		return err
	}
	if err := s.BindInt64(2, room); err != nil {
		return err
	}
	if row, err := s.Step(); err != nil {
		return err
	} else if row {
		return errors.New("fastdb write: UPDATE returned a row")
	}
	return nil
}

func (t *fastLaneTx) InsertRichText(ctx context.Context, id int64, body, stamp string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := t.tx.Stmt(stmtInsertRichText)
	if err != nil {
		return err
	}
	defer s.Reset()
	if err := s.BindInt64(1, id); err != nil {
		return err
	}
	if err := s.BindText(2, body); err != nil {
		return err
	}
	if err := s.BindText(3, stamp); err != nil {
		return err
	}
	if err := s.BindText(4, stamp); err != nil {
		return err
	}
	if row, err := s.Step(); err != nil {
		return err
	} else if row {
		return errors.New("fastdb write: INSERT returned a row")
	}
	return nil
}

func (t *fastLaneTx) InsertAttachment(ctx context.Context, blob, id int64, stamp string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := t.tx.Stmt(stmtInsertAttachment)
	if err != nil {
		return err
	}
	defer s.Reset()
	if err := s.BindInt64(1, blob); err != nil {
		return err
	}
	if err := s.BindInt64(2, id); err != nil {
		return err
	}
	if err := s.BindText(3, stamp); err != nil {
		return err
	}
	if row, err := s.Step(); err != nil {
		return err
	} else if row {
		return errors.New("fastdb write: INSERT returned a row")
	}
	return nil
}

func (t *fastLaneTx) InsertSearchIndex(ctx context.Context, id int64, plain string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := t.tx.Stmt(stmtInsertSearchIndex)
	if err != nil {
		return err
	}
	defer s.Reset()
	if err := s.BindInt64(1, id); err != nil {
		return err
	}
	if err := s.BindText(2, plain); err != nil {
		return err
	}
	if row, err := s.Step(); err != nil {
		return err
	} else if row {
		return errors.New("fastdb write: INSERT returned a row")
	}
	return nil
}

func (t *fastLaneTx) BumpUnread(ctx context.Context, room, user int64, stamp, cutoff string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := t.tx.Stmt(stmtBumpUnread)
	if err != nil {
		return err
	}
	defer s.Reset()
	if err := s.BindText(1, stamp); err != nil {
		return err
	}
	if err := s.BindText(2, stamp); err != nil {
		return err
	}
	if err := s.BindInt64(3, room); err != nil {
		return err
	}
	if err := s.BindInt64(4, user); err != nil {
		return err
	}
	if err := s.BindText(5, cutoff); err != nil {
		return err
	}
	if row, err := s.Step(); err != nil {
		return err
	} else if row {
		return errors.New("fastdb write: UPDATE returned a row")
	}
	return nil
}

// ExecContext runs one arbitrary statement (the staged blob insert) through
// the direct lane, converting the positional args to typed binds. It is the
// UploadTx surface; the create path's own statements never take this route
// (they bind positionally through the typed methods above).
func (t *fastLaneTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := t.tx.Stmt(query)
	if err != nil {
		return nil, err
	}
	defer s.Reset()
	for i, a := range args {
		switch v := a.(type) {
		case nil:
			if err := s.BindNull(i + 1); err != nil {
				return nil, err
			}
		case string:
			if err := s.BindText(i+1, v); err != nil {
				return nil, err
			}
		case *string:
			if v == nil {
				if err := s.BindNull(i + 1); err != nil {
					return nil, err
				}
			} else if err := s.BindText(i+1, *v); err != nil {
				return nil, err
			}
		case int64:
			if err := s.BindInt64(i+1, v); err != nil {
				return nil, err
			}
		case int:
			if err := s.BindInt64(i+1, int64(v)); err != nil {
				return nil, err
			}
		case []byte:
			if err := s.BindTextBytes(i+1, v); err != nil {
				return nil, err
			}
		case bool:
			n := int64(0)
			if v {
				n = 1
			}
			if err := s.BindInt64(i+1, n); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("fastdb write: unsupported argument type %T", a)
		}
	}
	row, err := s.Step()
	if err != nil {
		return nil, err
	}
	if row {
		return nil, errors.New("fastdb write: statement returned rows")
	}
	return directResult{id: t.tx.LastInsertID(), changes: t.tx.Changes()}, nil
}

func (t *fastLaneTx) commit() error {
	return t.tx.Commit()
}

func (t *fastLaneTx) rollback() error {
	return t.tx.Rollback()
}

func (t *fastLaneTx) savepoint() error {
	return t.tx.Exec("SAVEPOINT w")
}

func (t *fastLaneTx) rollbackTo() error {
	return t.tx.Exec("ROLLBACK TO w")
}

func (t *fastLaneTx) releaseSavepoint() error {
	return t.tx.Exec("RELEASE w")
}

// directResult adapts one direct execution's rowid and change count to
// sql.Result, the shape *sql.Tx.ExecContext returns on the database/sql
// lane (the stager only reads LastInsertId).
type directResult struct {
	id, changes int64
}

func (r directResult) LastInsertId() (int64, error) { return r.id, nil }
func (r directResult) RowsAffected() (int64, error) { return r.changes, nil }
