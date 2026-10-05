# once-campfire-go

Port of the Rust Campfire in `reference/`, pinned as a submodule. The Rails source is
`reference/reference/`. Preserve the SQLite schema, storage layout, cookies, frontend,
and behavior of the Rust app. Record intentional differences in README.md.

Use the Go standard library first: net/http, html/template, crypto, encoding/json, testing, and
embed. SQLite goes through the vendored crawshaw.io/sqlite (third_party/sqlite), not database/sql.
No web framework, ORM, dependency injection framework, or JavaScript build framework. Small
protocol/algorithm libraries are appropriate where stdlib has no implementation (SQLite, bcrypt,
WebSockets, HTML parsing).

Do not edit reference/. Port-owned frontend overrides go in assets/overrides/.
Tests live beside code. Reuse reference/vectors and the reference parity seed.
Never benchmark an incomplete response as if it were the full application.
Use identical data, response validation, CPU affinity, encoding, warmup, repetitions,
and sequential interleaved runs. Record raw measurements and limitations in bench/results/.

Use `mise exec go@1.27.1 -- go ...` if Go is not on PATH.
Run gofmt, go vet ./..., and go test -race ./... before committing.
SQLite builds use CGO_ENABLED=1.
