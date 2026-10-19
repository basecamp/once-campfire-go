# csqlite

Minimal direct-SQLite binding used by `internal/fastdb`. It provides only the
C API calls the read and write layers need: `sqlite3_open_v2`, `prepare_v2`,
`bind_*`, `step`, `column_*`, `reset`, `clear_bindings`, `finalize` and
`busy_timeout`, plus three fused helpers that perform a whole observation or
write sequence in one cgo transition:

- `Stmt.DataVersion` — step, column read and reset for `PRAGMA data_version`,
  the generation gate's per-request observation (three transitions per read
  before the fusion, one now).
- `Stmt.StepRow` — step and capture of the row's columns into a C row buffer
  owned by the statement, read back through `CellType`/`CellInt64`/`CellBytes`.
  The auth fast path's ten-column join read costs four transitions per warm
  request (`clear_bindings`, `bind_text`, `step_row`, the release reset)
  instead of one per column access.
- `Stmt.ExecTyped` — `fastdb_write_run`: typed binds, step,
  last-insert-rowid capture and reset for one write statement (the message and
  rich-text INSERTs and their siblings), one transition instead of one per
  bind/step/reset.

The capture order per column is the one the live accessors use — type, then the
integer, then bytes followed by text — so a captured cell reports exactly what
`ColumnType`/`ColumnInt64`/`ColumnBytes` report for the same row; a statement
wider than the buffer is refused rather than truncated.

## Where SQLite comes from

The SQLite implementation is not built here. `sqlite3-binding.h` is the
amalgamation header that ships with the pinned
[`github.com/mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3) module
(`v1.14.52`, see `go.mod`), and the compiled symbols come from that driver's
amalgamation in the same binary. A binary that links this package must also
link go-sqlite3; the application does, through `internal/database`. If nothing
links the driver the C symbols are missing at link time.

SQLite is in the public domain: https://sqlite.org/copyright.html.

## Same-module-version rule

Header and driver must be the same version. When the `go-sqlite3` pin in
`go.mod` changes, refresh the vendored copy and re-run the fastdb tests:

```sh
cp "$(go env GOMODCACHE)/github.com/mattn/go-sqlite3@<version>/sqlite3-binding.h" \
   internal/fastdb/csqlite/sqlite3-binding.h
```

Keep the vendored-file comment at the top of the header. Do not hand-edit the
declarations.

## CGO safety boundary

- Go paths, SQL text and pragma text are passed to C for the duration of the
  call only; `sqlite3_prepare_v2` keeps its own copy of the SQL.
- `BindText`/`BindTextBytes` bind with `SQLITE_TRANSIENT`, so SQLite copies
  the bytes before returning; `runtime.KeepAlive` pins the Go argument across
  the call. `SQLITE_STATIC` is never used.
- `ColumnBytes` returns a view of SQLite-owned memory that is valid only until
  the next `Step`, `Reset` or `Finalize`. `ColumnText` and fastdb's
  `ColumnTextInto` copy into Go memory first.
- `StepRow` captures into a C row buffer owned by the statement (allocated on
  first use, freed by `Finalize`), so no Go pointer crosses into C and no Go
  object holds SQLite memory. `CellBytes` exposes the same
  valid-until-`Step`/`Reset` view as `ColumnBytes`, and the fastdb decode copies
  or decodes it before the statement is reset.
- `ExecTyped` binds its text arguments through the same `SQLITE_TRANSIENT`
  helper, so SQLite copies them during the call; `runtime.KeepAlive` pins each
  string across it. Its `fastdb_write_run` reads `sqlite3_last_insert_rowid`
  (an integer, not a row buffer) after the step and before the reset, and
  returns without resetting on a bind or step failure so the caller can map
  `sqlite3_errmsg` while it is still current.
- No Go pointer is stored in C memory at any point.
