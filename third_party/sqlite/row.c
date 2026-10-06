#include <stdlib.h>

#include "row.h"

campfire_row *campfire_row_new(sqlite3_stmt *stmt) {
	campfire_row *row = calloc(1, sizeof(campfire_row));
	if (row == NULL) {
		return NULL;
	}
	row->columns = sqlite3_column_count(stmt);
	if (row->columns > 0) {
		row->values = calloc(row->columns, sizeof(campfire_value));
		if (row->values == NULL) {
			free(row);
			return NULL;
		}
		row->cap = row->columns;
	}
	return row;
}

void campfire_row_free(campfire_row *row) {
	if (row != NULL) {
		free(row->values);
		free(row);
	}
}

/* campfire_step is sqlite3_step that, when it returns a row, also reads the row's columns into
   row: a row then costs Go one cgo call, where reading each column from Go costs two or three.
   Each column is read with the accessor for its own type, so no value is converted. */
int campfire_step(sqlite3_stmt *stmt, campfire_row *row) {
	row->count = 0;
	int rc = sqlite3_step(stmt);
	row->columns = sqlite3_column_count(stmt);
	if (rc != SQLITE_ROW) {
		return rc;
	}
	int count = sqlite3_data_count(stmt);
	if (count > row->cap) {
		count = row->cap;
	}
	for (int i = 0; i < count; i++) {
		campfire_value *v = &row->values[i];
		v->type = sqlite3_column_type(stmt, i);
		switch (v->type) {
		case SQLITE_INTEGER:
			v->i = sqlite3_column_int64(stmt, i);
			break;
		case SQLITE_FLOAT:
			v->f = sqlite3_column_double(stmt, i);
			break;
		case SQLITE3_TEXT:
			v->p = sqlite3_column_text(stmt, i);
			v->n = sqlite3_column_bytes(stmt, i);
			break;
		case SQLITE_BLOB:
			v->p = sqlite3_column_blob(stmt, i);
			v->n = sqlite3_column_bytes(stmt, i);
			break;
		}
	}
	row->count = count;
	return rc;
}
