package write

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestRollbackFailureQuarantinesWriter(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "failed commit cleanup"}[commit], func(t *testing.T) {
			c, err := OpenWriter(filepath.Join(t.TempDir(), "db.sqlite3"), 32)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			tx, err := c.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			// End SQLite's transaction behind the wrapper to fault its next ROLLBACK.
			if err = tx.Exec("COMMIT"); err != nil {
				t.Fatal(err)
			}
			if commit {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err == nil {
				t.Fatal("expected rollback failure")
			}
			next, err := c.Begin(context.Background())
			if err == nil {
				next.Rollback()
				t.Fatal("failed-rollback handle reused")
			}
			if !errors.Is(err, ErrWriterClosed) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
