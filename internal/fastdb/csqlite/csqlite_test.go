package csqlite

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	// The csqlite test binary must link the SQLite amalgamation the same way
	// an application binary does; without the driver import the cgo calls in
	// csqlite.go are unresolved at link time.
	_ "github.com/mattn/go-sqlite3"
)

// TestVendoredHeaderMatchesModulePin makes the refresh rule executable: the
// vendored header comment must name the exact go.mod version of the driver
// whose amalgamation supplies the linked symbols, so a version bump that
// forgets to refresh the header fails here instead of at runtime.
func TestVendoredHeaderMatchesModulePin(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`github\.com/mattn/go-sqlite3 v(\S+)`).FindSubmatch(mod)
	if match == nil {
		t.Fatal("go.mod does not pin github.com/mattn/go-sqlite3")
	}
	version := "v" + string(match[1])

	header, err := os.ReadFile(filepath.Join(dir, "internal", "fastdb", "csqlite", "sqlite3-binding.h"))
	if err != nil {
		t.Fatal(err)
	}
	want := "github.com/mattn/go-sqlite3 version " + version
	if !strings.Contains(string(header), want) {
		t.Fatalf("vendored sqlite3-binding.h does not record %q; refresh it from the module cache and update the comment", want)
	}
}
