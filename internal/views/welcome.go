package views

// Views for reference/app/views/welcome (the reference's welcome.rs).

// WelcomeShow is welcome/show.html.erb: shown to users who aren't in any room yet.
type WelcomeShow struct {
	PageBase
	Ctx *ViewContext
	// Current.user.name.
	CurrentUserName string
}

func (p *WelcomeShow) PageTitle() *string { return Ptr("No rooms yet") }
func (p *WelcomeShow) BodyClass() *string { return Ptr("sidebar") }
