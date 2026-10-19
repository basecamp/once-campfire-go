package write

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestWALHookTracksCommits pins the write lane's WAL-growth signal (Lever 4):
// the hook reports the WAL's frame count after each commit on this connection,
// so the writer can bound the WAL without a statement or an allocation on the
// commit path. The counter must move with real commits and CheckpointRestart
// must reset the WAL (the next write starts a fresh log).
func TestWALHookTracksCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.sqlite3")
	conn, err := OpenWriter(path, 16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx := context.Background()
	insert := func(rows int) {
		t.Helper()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Exec("CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY AUTOINCREMENT, a TEXT)"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < rows; i++ {
			if err := tx.Exec("INSERT INTO t(a) VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')"); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	insert(50)
	first := conn.WALFrames()
	if first <= 0 {
		t.Fatalf("WALFrames after a commit = %d, want > 0", first)
	}
	insert(50)
	if second := conn.WALFrames(); second <= first {
		t.Fatalf("WALFrames did not grow with commits: %d then %d", first, second)
	}
	// The restart resets the WAL, so the next commit reports a small count
	// again; the file itself is not truncated (journal_size_limit stays -1),
	// only its write position moves back to the first frame.
	if result, err := conn.CheckpointRestart(); err != nil {
		t.Fatal(err)
	} else if !result.Completed {
		t.Fatalf("CheckpointRestart reported incomplete with no reader pinning: %+v", result)
	}
	insert(1)
	if after := conn.WALFrames(); after >= first {
		t.Fatalf("WALFrames after CheckpointRestart = %d, want below the pre-restart %d", after, first)
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() == 0 {
		t.Fatal("CheckpointRestart truncated the WAL; it must only restart it")
	}
}
