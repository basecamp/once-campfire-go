// VersionReader reads SQLite's connection-local PRAGMA data_version on a
// dedicated read-only handle with no per-call Go allocations: the statement
// is prepared once and each read is one fused step/column/reset transition in
// C. The handle is a separate connection, so it observes commits made by
// every other connection — including other processes — which is exactly the
// external-writer observation the generation gates need.
package csqlite

import "sync"

// VersionReader is a prepared PRAGMA data_version reader. The statement is
// not safe for concurrent use, so reads serialize on one mutex: the critical
// section is a single fused read, and reads are per request or per publish,
// never per element.
type VersionReader struct {
	mu   sync.Mutex
	conn *Conn
	stmt *Stmt
}

// OpenVersionReader opens path read-only and prepares the pragma.
func OpenVersionReader(path string) (*VersionReader, error) {
	conn, err := OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	stmt, err := conn.Prepare("PRAGMA data_version")
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &VersionReader{conn: conn, stmt: stmt}, nil
}

// Read returns the current data_version. A successful read is one cgo
// transition and allocates nothing: the step, column read and reset happen
// inside fastdb_data_version.
func (v *VersionReader) Read() (uint64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.stmt.DataVersion()
}

// Close finalizes the statement and closes the handle.
func (v *VersionReader) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stmt != nil {
		_ = v.stmt.Finalize()
		v.stmt = nil
	}
	if v.conn != nil {
		err := v.conn.Close()
		v.conn = nil
		return err
	}
	return nil
}
