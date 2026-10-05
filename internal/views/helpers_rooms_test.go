package views

import "testing"

// The rooms helpers as the reference renders them (tests/golden).
func TestRoomsHelpersLikeRails(t *testing.T) {
	ctx := testContext()
	got := ButtonToChangeInvolvement(ctx, InvolvementRoom{ID: 104393281, ParamKey: "rooms_open"}, "everything")
	want := `<form class="button_to" method="post" action="/rooms/104393281/involvement?involvement=nothing"><input type="hidden" name="_method" value="put" /><button role="checkbox" aria-checked="true" aria-labelledby="involvement_label_rooms_open_104393281" tabindex="0" class="btn everything" type="submit"><img aria-hidden="true" src="/assets/notification-bell-everything-cde41b14.svg" width="20" height="20" /><span class="for-screen-reader" id="involvement_label_rooms_open_104393281">Notifying about all messages</span></button></form>`
	if got != HTML(want) {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	got = LinkToRoom("x", 486777696, NewAttrs().ID(DomID("rooms_closed", 486777696, "list")).Data("sorted_list_name", "All Talk").Style("--column-gap: 0.5em").Class("align-center gap room btn txt-nowrap"))
	want = `<a id="list_rooms_closed_486777696" data-rooms-list-target="room" data-room-id="486777696" data-badge-dot-target="unread" data-sorted-list-target="item" data-sorted-list-name="All Talk" style="--column-gap: 0.5em" class="align-center gap room btn txt-nowrap" href="/rooms/486777696">x</a>`
	if got != HTML(want) {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	got = LinkToRoom("x", 186869642, NewAttrs().Class("direct").ID(DomID("rooms_direct", 186869642, "list")).Data("sorted_list_number", "1790427620300"))
	want = `<a class="direct" id="list_rooms_direct_186869642" data-rooms-list-target="room" data-room-id="186869642" data-badge-dot-target="unread" data-sorted-list-target="item" data-sorted-list-number="1790427620300" href="/rooms/186869642">x</a>`
	if got != HTML(want) {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	for _, c := range []struct {
		direct      bool
		involvement string
		want        string
	}{
		{false, "mentions", "everything"},
		{false, "invisible", "mentions"},
		{true, "everything", "nothing"},
		{true, "nothing", "everything"},
		{true, "mentions", "everything"},
	} {
		if got := NextInvolvement(c.direct, c.involvement); got != c.want {
			t.Errorf("NextInvolvement(%v, %s) = %s", c.direct, c.involvement, got)
		}
	}
}
