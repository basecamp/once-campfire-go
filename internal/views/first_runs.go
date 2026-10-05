package views

// Views for reference/app/views/first_runs (the reference's first_runs.rs).

// FirstRunsShow is first_runs/show.html.erb: account setup, shown until the first user exists.
type FirstRunsShow struct {
	PageBase
	Ctx *ViewContext
}

func (p *FirstRunsShow) PageTitle() *string { return Ptr("Set up Campfire") }
func (p *FirstRunsShow) BodyClass() *string { return Ptr("signup") }
