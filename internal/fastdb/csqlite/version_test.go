package csqlite

import (
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"

	// The test binary must link the SQLite amalgamation, exactly as an
	// application binary does; the driver supplies the symbols csqlite calls.
	_ "github.com/mattn/go-sqlite3"
)

// versionTestDB creates a database file the version tests can read. SQLite
// opens read-only handles lazily, so the file must exist with a schema.
func versionTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "version.sqlite3")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestVersionReadCgoBudget pins the warm-hit cgo transition budget of one
// data_version observation. The unfused read was three transitions per
// observation (Step, ColumnInt64, Reset) — six per request at the two
// observations the generation gates make. fastdb_data_version fuses
// step/column/reset into one C call, so the budget is one, and a regression
// that re-splits the helper (or adds a second crossing) fails here.
func TestVersionReadCgoBudget(t *testing.T) {
	v, err := OpenVersionReader(versionTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	// Warm: the first read must not be the measured one.
	if _, err := v.Read(); err != nil {
		t.Fatal(err)
	}

	before := runtime.NumCgoCall()
	if _, err := v.Read(); err != nil {
		t.Fatal(err)
	}
	if got := runtime.NumCgoCall() - before; got != 1 {
		t.Fatalf("warm VersionReader.Read crossed into C %d times, want 1 (unfused: 3)", got)
	}

	if allocs := testing.AllocsPerRun(200, func() {
		if _, err := v.Read(); err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("VersionReader.Read allocated %v objects per call, want 0", allocs)
	}
}

// TestVersionReadObservesCommits holds the fused read to the semantics of the
// sequence it replaced: repeated reads with no writer are stable, and a commit
// from another connection moves the value.
func TestVersionReadObservesCommits(t *testing.T) {
	path := versionTestDB(t)
	v, err := OpenVersionReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	first, err := v.Read()
	if err != nil {
		t.Fatal(err)
	}
	again, err := v.Read()
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("data_version moved with no commit: %d then %d", first, again)
	}

	w, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Exec("INSERT INTO t(x) VALUES (1)"); err != nil {
		t.Fatal(err)
	}

	after, err := v.Read()
	if err != nil {
		t.Fatal(err)
	}
	if after == first {
		t.Fatalf("fused read did not observe another connection's commit: still %d", after)
	}
}
