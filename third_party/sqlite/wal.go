package sqlite

// #include <sqlite3.h>
// #include <stdlib.h>
//
// static int note_wal_pages(void *slot, sqlite3 *db, const char *name, int pages) {
//	*(int *)slot = pages;
//	return SQLITE_OK;
// }
//
// static int *install_wal_note(sqlite3 *db) {
//	int *slot = calloc(1, sizeof(int));
//	if (slot != NULL) {
//		sqlite3_wal_hook(db, note_wal_pages, slot);
//	}
//	return slot;
// }
import "C"

// NoteWALPages replaces the connection's WAL hook, and with it SQLite's auto-checkpoint (which
// is itself a WAL hook, sqlite3_wal_autocheckpoint), with one that only records the WAL's size in
// pages after each commit. TakeWALPages reads it. The hook runs in C, without calling into Go.
//
// https://www.sqlite.org/c3ref/wal_hook.html
func (conn *Conn) NoteWALPages() error {
	if conn.walPages != nil {
		return nil
	}
	slot := C.install_wal_note(conn.conn)
	if slot == nil {
		return reserr("Conn.NoteWALPages", "", "out of memory", C.SQLITE_NOMEM)
	}
	conn.walPages = slot
	return nil
}

// TakeWALPages returns the WAL size noted by the latest commit since the previous call, or 0 when
// nothing was committed in between.
func (conn *Conn) TakeWALPages() int {
	if conn.walPages == nil {
		return 0
	}
	pages := int(*conn.walPages)
	*conn.walPages = 0
	return pages
}
