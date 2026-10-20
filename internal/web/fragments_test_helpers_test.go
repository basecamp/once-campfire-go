package web

import "github.com/basecamp/once-campfire-go/internal/presentation"

func setFragmentLimit(app *testRuntime, limit int) {
	app.Fragments = presentation.NewFragments(app.Presentation, limit)
	app.MessageQueries.Fragments = app.Fragments
}
