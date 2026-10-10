package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memstatusFlag compiles SQLite (inside mattn/go-sqlite3, through CGO_CFLAGS) without memory
// statistics. With them on, SQLite takes one process-wide mutex (mem0.mutex) around every
// sqlite3_malloc and sqlite3_free to keep its counters, so concurrent requests on separate read
// connections serialize on that one lock even though nothing reads the counters. The flag cannot
// be set from this module's cgo directives (they only reach this module's own C code), so it lives
// in every build entry point, and this test keeps those from drifting.
const memstatusFlag = "-DSQLITE_DEFAULT_MEMSTATUS=0"

func TestBuildEntryPointsDisableSQLiteMemstatus(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, file := range []string{"bin/build", "bin/check", "Dockerfile"} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			// Go's default CGO_CFLAGS is "-O2 -g"; setting the variable replaces it, so the
			// optimisation flags must stay on the same line.
			if strings.Contains(line, "CGO_CFLAGS=") && strings.Contains(line, memstatusFlag) && strings.Contains(line, "-O2") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not build SQLite with CGO_CFLAGS containing -O2 and %s", file, memstatusFlag)
		}
	}
}
