package views

// Views for reference/app/views/sessions (the reference's sessions.rs).

// BrowserVersion is a minimum browser version AllowBrowser lets in.
type BrowserVersion struct{ Browser, Version string }

// AllowBrowserVersions is AllowBrowser::VERSIONS, minus the browsers it blocks outright (ie: false).
var AllowBrowserVersions = [4]BrowserVersion{{"safari", "17.2"}, {"chrome", "120"}, {"firefox", "121"}, {"opera", "104"}}

// SessionsNew is sessions/new.html.erb.
type SessionsNew struct {
	PageBase
	Ctx *ViewContext
	// params[:email_address].
	EmailAddress *string
	// User.administrator.first, for accounts/_help_contact.
	HelpContact *HelpContact
}

func (p *SessionsNew) PageTitle() *string { return Ptr("Sign in") }

// SessionsIncompatibleBrowser is sessions/incompatible_browser.html.erb, rendered by AllowBrowser
// for old browsers.
type SessionsIncompatibleBrowser struct {
	PageBase
	Ctx *ViewContext
}

func (p *SessionsIncompatibleBrowser) PageTitle() *string {
	if p.Ctx.Platform.AppleMessages {
		return Ptr("Campfire")
	}
	return Ptr("Unsupported browser")
}

// SessionsTransfersShow is sessions/transfers/show.html.erb: an auto-submitting form that PUTs back
// to the page's own URL (url_for({}), i.e. session_transfer_path(id)).
type SessionsTransfersShow struct {
	PageBase
	Ctx *ViewContext
	// The request path, session_transfer_path(params[:id]).
	Action string
}
