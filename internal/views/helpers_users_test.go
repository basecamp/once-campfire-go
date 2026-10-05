package views

import "testing"

func TestComputesInitialsLikeRuby(t *testing.T) {
	if got := Initials("Élodie Ünal-Smith o'Brien 3po _x ñ"); got != "SoB3_" {
		t.Errorf("got %s", got)
	}
	if got := Initials("David Heinemeier Hansson"); got != "DHH" {
		t.Errorf("got %s", got)
	}
}

func TestAvatarColorsLikeZlibCRC32(t *testing.T) {
	// AVATAR_COLORS[Zlib.crc32(id.to_s) % 18] in the reference.
	for id, index := range map[int64]int{1: 11, 2: 13, 42: 8, 1000: 3} {
		if got := AvatarBackgroundColor(id); got != AvatarColors[index] {
			t.Errorf("%d: got %s", id, got)
		}
	}
}

// The users helpers as the reference renders them (tests/golden).
func TestUsersHelpersLikeRails(t *testing.T) {
	ctx := testContext()
	ctx.Account.LogoURL = "/account/logo?v=20260926130020"
	sidebar := RouteUserSidebar()
	for _, c := range []struct{ got, want HTML }{
		{AvatarTag(ctx, AvatarUser{ID: 394959859, Title: "Bender Bot", AvatarPath: "/users/x/avatar?v=1"}, NewAttrs().Loading("lazy")), `<a title="Bender Bot" class="btn avatar" data-turbo-frame="_top" href="/users/394959859"><img aria-hidden="true" loading="lazy" src="/users/x/avatar?v=1" width="48" height="48" /></a>`},
		{AccountLogoTag(ctx, ""), `<figure class="account-logo avatar "><img alt="Account logo" src="/account/logo?v=20260926130020" width="300" height="300" /></figure>`},
		{ProfileFormSubmitButton(ctx), `<button class="btn btn--reversed center txt-large" type="submit"><img aria-hidden="true" src="/assets/check-7897ff7e.svg" width="20" height="20" /><span class="for-screen-reader">Save changes</span></button>`},
		{ButtonToDirectRoomWith(ctx, 394959859), `<form class="button_to" method="post" action="/rooms/directs?user_ids%5B%5D=394959859"><button class="btn btn--primary full-width txt--large" type="submit"><img src="/assets/messages-9395d503.svg" /></button></form>`},
		{SidebarTurboFrameTag(&sidebar, ""), `<turbo-frame data-turbo-permanent="true" data-controller="rooms-list read-rooms turbo-frame" data-rooms-list-unread-class="unread" data-action="presence:present@window->rooms-list#read read-rooms:read->rooms-list#read turbo:frame-load->rooms-list#loaded refresh-room:visible@window->turbo-frame#reload" id="user_sidebar" src="/users/me/sidebar" target="_top"></turbo-frame>`},
		{SidebarTurboFrameTagBlock("x"), `<turbo-frame data-turbo-permanent="true" data-controller="rooms-list read-rooms turbo-frame" data-rooms-list-unread-class="unread" data-action="presence:present@window->rooms-list#read read-rooms:read->rooms-list#read turbo:frame-load->rooms-list#loaded refresh-room:visible@window->turbo-frame#reload" id="user_sidebar" target="_top">x</turbo-frame>`},
		{UserFilterSearchTag(), `<input type="search" id="search" autocorrect="off" autocomplete="off" data-1p-ignore="true" class="input input--transparent full-width" placeholder="Filter…" data-action="input-&gt;filter#filter">`},
	} {
		if c.got != c.want {
			t.Errorf("got  %s\nwant %s", c.got, c.want)
		}
	}
	bio := " "
	if got := UserTitle("Anna", &bio); got != "Anna" {
		t.Errorf("got %q", got)
	}
	bio = "Hi"
	if got := UserTitle("Anna", &bio); got != "Anna – Hi" {
		t.Errorf("got %q", got)
	}
}
