// The writer's WAL hook (see wal.go). SQLite's public API, declared here because
// go-sqlite3 links the amalgamation without installing its header for other packages.
#include "_cgo_export.h"

typedef struct sqlite3 sqlite3;
typedef struct sqlite3_context sqlite3_context;
typedef struct sqlite3_value sqlite3_value;
typedef struct sqlite3_api_routines sqlite3_api_routines;
int sqlite3_auto_extension(void (*entry)(void));
int sqlite3_create_function_v2(sqlite3 *, const char *, int, int, void *,
	void (*)(sqlite3_context *, int, sqlite3_value **), void (*)(sqlite3_context *),
	void (*)(sqlite3_context *), void (*)(void *));
sqlite3 *sqlite3_context_db_handle(sqlite3_context *);
void sqlite3_result_null(sqlite3_context *);
void *sqlite3_wal_hook(sqlite3 *, int (*)(void *, sqlite3 *, const char *, int), void *);
int sqlite3_wal_checkpoint_v2(sqlite3 *, const char *, int, int *, int *);
const char *sqlite3_db_filename(sqlite3 *, const char *);

#define CAMPFIRE_UTF8 1
#define CAMPFIRE_DIRECTONLY 0x000080000
#define CAMPFIRE_CHECKPOINT_PASSIVE 0
#define CAMPFIRE_CHECKPOINT_RESTART 2

// Runs on the committing thread after each commit, in place of the auto-checkpoint
// (itself a WAL hook) that installing this replaces.
static int campfire_wal_hook(void *unused, sqlite3 *db, const char *schema, int pages) {
	const char *path = sqlite3_db_filename(db, schema);
	switch (campfireWALCommitted((char *)path, pages)) {
	case 1:
		sqlite3_wal_checkpoint_v2(db, schema, CAMPFIRE_CHECKPOINT_RESTART, 0, 0);
		campfireWALRestarted((char *)path);
		break;
	case 2:
		sqlite3_wal_checkpoint_v2(db, schema, CAMPFIRE_CHECKPOINT_PASSIVE, 0, 0);
		break;
	}
	return 0;
}

// SELECT campfire_wal_hook() installs the hook on the connection that runs it. SQLite
// sets the default auto-checkpoint after running auto-extensions, so the writer's
// connect hook calls this once the connection is open.
static void campfire_install(sqlite3_context *context, int argc, sqlite3_value **argv) {
	sqlite3_wal_hook(sqlite3_context_db_handle(context), campfire_wal_hook, 0);
	sqlite3_result_null(context);
}

static int campfire_connection(sqlite3 *db, char **error, const sqlite3_api_routines *api) {
	return sqlite3_create_function_v2(db, "campfire_wal_hook", 0, CAMPFIRE_UTF8 | CAMPFIRE_DIRECTONLY, 0,
		campfire_install, 0, 0, 0);
}

int campfire_register_wal_hook(void) {
	return sqlite3_auto_extension((void (*)(void))campfire_connection);
}
