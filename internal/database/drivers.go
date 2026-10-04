package database

import (
	"database/sql"
	"sync"

	"github.com/mattn/go-sqlite3"
)

// Connections keep temporary B-trees (DISTINCT, ORDER BY without an index) in
// memory. They are small here; with the default file store each one cost far more
// than the rows it sorted (a 10-row sidebar query took ~20 µs instead of ~6).
// The writer's connections also install the WAL hook (wal.go).
const (
	readerDriver = "sqlite3_campfire"
	writerDriver = "sqlite3_campfire_writer"
)

var (
	registerDriversOnce sync.Once
	errRegisterDrivers  error
)

func registerDrivers() error {
	registerDriversOnce.Do(func() {
		if errRegisterDrivers = registerWALHook(); errRegisterDrivers != nil {
			return
		}
		configure := func(conn *sqlite3.SQLiteConn, statements ...string) error {
			for _, statement := range statements {
				if _, err := conn.Exec(statement, nil); err != nil {
					return err
				}
			}
			return nil
		}
		sql.Register(readerDriver, &uncancelledDriver{sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return configure(conn, "PRAGMA temp_store=MEMORY")
		}}})
		sql.Register(writerDriver, &uncancelledDriver{sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return configure(conn, "PRAGMA temp_store=MEMORY", "SELECT campfire_wal_hook()")
		}}})
	})
	return errRegisterDrivers
}
