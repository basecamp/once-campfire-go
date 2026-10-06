# csqlite

Minimal direct-SQLite binding used by `internal/fastdb`. It provides only the
C API calls the read layer needs: `sqlite3_open_v2`, `prepare_v2`, `bind_*`,
`step`, `column_*`, `reset`, `clear_bindings`, `finalize` and `busy_timeout`.

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
- No Go pointer is stored in C memory at any point.
