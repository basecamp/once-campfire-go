package views

import (
	"hash/crc32"
	"strconv"
	"strings"
	"unicode"
)

// UsersHelper, Users::AvatarsHelper, Users::FilterHelper, Users::ProfilesHelper,
// Users::SidebarHelper and AccountsHelper.

// AvatarColors is Users::AvatarsHelper::AVATAR_COLORS.
var AvatarColors = [18]string{
	"#AF2E1B", "#CC6324", "#3B4B59", "#BFA07A", "#ED8008", "#ED3F1C", "#BF1B1B", "#736B1E", "#D07B53", "#736356", "#AD1D1D", "#BF7C2A",
	"#C09C6F", "#698F9C", "#7C956B", "#5D618F", "#3B3633", "#67695E",
}

// AvatarBackgroundColor is avatar_background_color(user): Zlib.crc32(user.to_param) picks the color.
func AvatarBackgroundColor(userID int64) string {
	crc := crc32.ChecksumIEEE([]byte(strconv.FormatInt(userID, 10)))
	return AvatarColors[crc%uint32(len(AvatarColors))]
}

// Initials is User#initials: `name.scan(/\b\w/).join`. Ruby's \w is ASCII-only while \b sees
// Unicode word characters, so "Élodie" contributes nothing.
func Initials(name string) string {
	var b strings.Builder
	previousIsWord := false
	for _, c := range name {
		if (isASCIIAlphanumeric(c) || c == '_') && !previousIsWord {
			b.WriteRune(c)
		}
		previousIsWord = isWordChar(c)
	}
	return b.String()
}

// isWordChar is Rust's char::is_alphanumeric (the Alphabetic and Numeric properties) or "_".
func isWordChar(c rune) bool {
	return c == '_' || unicode.In(c, unicode.L, unicode.N, unicode.Other_Alphabetic, unicode.Other_Lowercase, unicode.Other_Uppercase)
}

// UserTitle is User#title: `[ name, bio ].compact_blank.join(" – ")`.
func UserTitle(name string, bio *string) string {
	parts := make([]string, 0, 2)
	if strings.TrimSpace(name) != "" {
		parts = append(parts, name)
	}
	if bio != nil && strings.TrimSpace(*bio) != "" {
		parts = append(parts, *bio)
	}
	return strings.Join(parts, " – ")
}

// AvatarUser is what avatar_tag needs to know about a user.
type AvatarUser struct {
	ID int64
	// User#title.
	Title string
	// fresh_user_avatar_path(user).
	AvatarPath string
}

// AvatarTag is avatar_tag(user, **options): the options go to the image.
func AvatarTag(ctx *ViewContext, user AvatarUser, options *Attrs) HTML {
	image := ImageTag(ctx, user.AvatarPath, NewAttrs().AriaHidden().Size(48).Merge(options))
	return LinkTo(RouteUser(user.ID), NewAttrs().Title(user.Title).Class("btn avatar").Data("turbo_frame", "_top"), image)
}

// ButtonToDirectRoomWith is button_to_direct_room_with(user).
func ButtonToDirectRoomWith(ctx *ViewContext, userID int64) HTML {
	return ButtonTo(RoomsDirectsWithUsers([]int64{userID}),
		NewAttrs().Class("btn btn--primary full-width txt--large"),
		ImageTag(ctx, "messages.svg", nil))
}

// CurlTextLine and CurlUploadLine are the bot curl commands in accounts/bots/_bot.
func CurlTextLine(url string) string { return "curl -d 'Hello!' " + url }

func CurlUploadLine(url string) string { return `curl -F "attachment=@/path/to/file" ` + url }

// AccountLogoTag is account_logo_tag(style:). A nil style ("") leaves a trailing space in the class.
func AccountLogoTag(ctx *ViewContext, style string) HTML {
	image := ImageTag(ctx, ctx.Account.LogoURL, NewAttrs().Alt("Account logo").Size(300))
	return ContentTag("figure", NewAttrs().Class("account-logo avatar "+style), image)
}

// ProfileFormSubmitButton is profile_form_submit_button.
func ProfileFormSubmitButton(ctx *ViewContext) HTML {
	content := ImageTag(ctx, "check.svg", NewAttrs().AriaHidden().Size(20)) +
		ContentTagText("span", NewAttrs().Class("for-screen-reader"), "Save changes")
	return ContentTag("button", NewAttrs().Class("btn btn--reversed center txt-large").Type("submit"), content)
}

// SidebarTurboFrameTag is sidebar_turbo_frame_tag(src:) { content }.
func SidebarTurboFrameTag(src *string, content HTML) HTML {
	return ContentTag("turbo-frame", sidebarTurboFrameOptions(src), content)
}

// sidebarTurboFrameOptions is sidebar_turbo_frame_tag's attributes: `data: { turbo_permanent: true,
// controller: ..., rooms_list_unread_class: ..., action: ... }` for turbo_frame_tag.
func sidebarTurboFrameOptions(src *string) *Attrs {
	data := NewAttrs().
		Attr("data-turbo-permanent", true).
		Attr("data-controller", "rooms-list read-rooms turbo-frame").
		Attr("data-rooms-list-unread-class", "unread").
		// html_safe in the reference so "->" isn't escaped
		Attr("data-action", HTML("presence:present@window->rooms-list#read read-rooms:read->rooms-list#read "+
			"turbo:frame-load->rooms-list#loaded refresh-room:visible@window->turbo-frame#reload"))
	return turboFrameOptions("user_sidebar", toValue(src), textAttr("_top"), data)
}

// UserFilterMenuTag is user_filter_menu_tag { content }.
func UserFilterMenuTag(content HTML) HTML {
	options := NewAttrs().
		Class("flex flex-column gap margin-none pad overflow-y constrain-height").
		Data("controller", "filter").
		Data("filter_active_class", "filter--active").
		Data("filter_selected_class", "selected")
	return ContentTag("menu", options, content)
}

// UserFilterSearchTag is user_filter_search_tag.
func UserFilterSearchTag() HTML {
	return BuilderTag("input", NewAttrs().
		Type("search").
		ID("search").
		Attr("autocorrect", "off").
		Autocomplete("off").
		Attr("data-1p-ignore", "true").
		Class("input input--transparent full-width").
		Placeholder("Filter…").
		Data("action", "input->filter#filter"))
}
