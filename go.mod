module github.com/basecamp/once-campfire-go

go 1.27.1

require (
	github.com/coder/websocket v1.8.15
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/sebishogun/simdhttp v0.0.0-20260826101408-b29e9d7285e8
	golang.org/x/crypto v0.57.1-0.20260918190515-b4dcfb54b863
	golang.org/x/net v0.59.0
)

require (
	github.com/sebishogun/simd v1.20.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

// Local transport extension shares immutable prepared frames between subscribers.
replace github.com/coder/websocket => ./third_party/websocket
