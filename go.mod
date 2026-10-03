module github.com/basecamp/once-campfire-go

go 1.27.1

require (
	github.com/coder/websocket v1.8.15
	github.com/mattn/go-sqlite3 v1.14.52
	golang.org/x/crypto v0.57.1-0.20260918190515-b4dcfb54b863
	golang.org/x/net v0.59.0
)

require golang.org/x/text v0.42.0 // indirect

// Local transport extension shares immutable prepared frames between subscribers.
replace github.com/coder/websocket => ./third_party/websocket
