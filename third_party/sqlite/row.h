#ifndef CAMPFIRE_ROW_H
#define CAMPFIRE_ROW_H

#include <sqlite3.h>

/* A column value of a statement's current row, read in C by campfire_step. */
typedef struct campfire_value {
	sqlite3_int64 i; /* SQLITE_INTEGER */
	double f;        /* SQLITE_FLOAT */
	const void *p;   /* SQLITE_TEXT and SQLITE_BLOB: valid until the statement steps or resets */
	int n;           /* bytes at p */
	int type;        /* sqlite3_column_type */
} campfire_value;

/* A statement's current row: the values of its first count columns (at most cap). count is 0
   when the statement has no current row. */
typedef struct campfire_row {
	int count;
	int cap;
	int columns; /* sqlite3_column_count as of the last step */
	campfire_value *values;
} campfire_row;

campfire_row *campfire_row_new(sqlite3_stmt *stmt);
void campfire_row_free(campfire_row *row);
int campfire_step(sqlite3_stmt *stmt, campfire_row *row);

#endif /* CAMPFIRE_ROW_H */
