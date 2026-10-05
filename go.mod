module github.com/basecamp/once-campfire-go

go 1.27.1

require (
	crawshaw.io/sqlite v0.3.2
	github.com/coder/websocket v1.8.15
	github.com/valyala/quicktemplate v1.8.0
	golang.org/x/crypto v0.57.1-0.20260918190515-b4dcfb54b863
	golang.org/x/net v0.59.0
)

require (
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

// Local transport extension shares immutable prepared frames between subscribers.
replace github.com/coder/websocket => ./third_party/websocket

// SQLite's C API directly (no database/sql), with the reference's SQLite 3.53.2; see
// third_party/sqlite/README.campfire.
replace crawshaw.io/sqlite => ./third_party/sqlite
