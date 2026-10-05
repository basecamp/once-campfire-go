package views

// ViewContext is the per-request state every page needs: what ApplicationController, the layout
// and the helpers read from Current, request, flash and the session (the reference's
// campfire_views::ViewContext). Optional values are nil when absent, as the reference's Options.
type ViewContext struct {
	CurrentUser *CurrentUser
	Account     AccountSummary
	FlashNotice *string
	FlashAlert  *string
	// ApplicationPlatform facts derived from the user agent.
	Platform Platform
	// Rails.configuration.x.vapid.public_key; nil omits the meta tag's content attribute.
	VAPIDPublicKey *string
	// AssetPath resolves a logical asset path ("campfire-icon.png") to its digested URL.
	AssetPath func(string) string
	// The `<script type="importmap">` + modulepreload tags (javascript_importmap_tags).
	ImportmapTags HTML
	// `<link rel="stylesheet">` tags for `stylesheet_link_tag :all, "data-turbo-track": "reload"`.
	StylesheetTags HTML
	// The account's custom CSS, if any (custom_styles_tag).
	CustomStyles *string
	// script_aware_action_cable_meta_tag content: script_name + "/cable".
	CableURL string
	// request.base_url ("http://campfire.test"), for the *_url helpers.
	BaseURL string
	// request.url, compared against the referrer by link_back.
	RequestURL string
	// request.referrer.
	Referrer *string
	// Id of last_room_visited (TrackedRoomVisit); nil links back to the root.
	LastRoomVisitedID *int64
	// Rails.application.config.app_version (APP_VERSION, GIT_REVISION or "0").
	AppVersion string
	// The fragment store templates cache into (the reference's thread-local current store); nil
	// renders uncached.
	Cache *FragmentCache
}

// Asset resolves a logical asset path.
func (c *ViewContext) Asset(path string) string { return c.AssetPath(path) }

// URL is root_url, session_url, join_url(...): base URL + path.
func (c *ViewContext) URL(path string) string { return c.BaseURL + path }

func (c *ViewContext) CanAdminister() bool {
	return c.CurrentUser != nil && c.CurrentUser.Administrator
}

// CurrentUserID is Current.user's id, if signed in.
func (c *ViewContext) CurrentUserID() (int64, bool) {
	if c.CurrentUser == nil {
		return 0, false
	}
	return c.CurrentUser.ID, true
}

// IsCurrentUser is `Current.user == user`.
func (c *ViewContext) IsCurrentUser(id int64) bool {
	return c.CurrentUser != nil && c.CurrentUser.ID == id
}

type CurrentUser struct {
	ID            int64
	Name          string
	Administrator bool
	Bot           bool
	// fresh_user_avatar_path(Current.user).
	AvatarURL string
}

type AccountSummary struct {
	Name string
	// fresh_account_logo_path (no size).
	LogoURL string
	// Current.account.logo.attached? (adds the account-has-logo body class).
	HasLogo bool
}

type Platform struct {
	IOS, Android, Mac, Windows, Chrome, Firefox, Safari, Edge, Mobile, Desktop bool
	// ApplicationPlatform#apple_messages?.
	AppleMessages bool
	// user_agent.browser from the useragent gem ("Chrome", "Safari", "Firefox", "Edge", ...).
	Browser string
	// ApplicationPlatform#operating_system ("macOS", "Windows", "iPhone", ...).
	OperatingSystem string
}
