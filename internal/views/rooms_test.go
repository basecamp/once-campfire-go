package views

import "testing"

func TestNamesRoomsLikeRoomDisplayName(t *testing.T) {
	david := Ptr("David")
	for _, c := range []struct {
		name   *string
		direct bool
		others []string
		want   string
	}{
		{Ptr("All Talk"), false, nil, "All Talk"},
		{nil, false, nil, ""},
		{nil, true, []string{"Jason", "Kevin"}, "Jason and Kevin"},
		{nil, true, []string{"Jason", "Kevin", "Jorge"}, "Jason, Kevin, and Jorge"},
		{nil, true, nil, "David"},
		{nil, true, []string{" "}, "David"},
	} {
		if got := RoomDisplayName(c.name, c.direct, c.others, david); got != c.want {
			t.Errorf("RoomDisplayName(%v, %v) = %q, want %q", c.direct, c.others, got, c.want)
		}
	}
}

func TestPostsRoomFormsToTheRoomsResource(t *testing.T) {
	for _, c := range []struct {
		id   *int64
		kind RoomKind
		want string
	}{
		{Ptr[int64](7), RoomKindOpen, "/rooms/opens/7"},
		{Ptr[int64](7), RoomKindClosed, "/rooms/closeds/7"},
		{nil, RoomKindOpen, "/rooms/opens"},
		{nil, RoomKindClosed, "/rooms/closeds"},
	} {
		room := FormRoom{ID: c.id}
		if got := room.Action(c.kind); got != c.want {
			t.Errorf("got %s, want %s", got, c.want)
		}
	}
}

func TestRoomFormPagesFillTheirLayoutsRoomForm(t *testing.T) {
	form := &OpenFormView{Room: FormRoom{ID: Ptr[int64](42), Name: Ptr("All Talk")}, CanAdminister: true}
	page := NewRoomsOpensEdit(testContext(), form)
	if page.Page != RoomFormPage(page) || page.RoomID() != 42 || !page.CanAdminister || page.Room.DisplayName() != "All Talk" {
		t.Errorf("layout not wired to the page: %+v", page.RoomsLayoutsEdit)
	}
	if got := *page.PageTitle(); got != "Edit settings for All Talk" {
		t.Errorf("got %s", got)
	}
	if page.BodyClass() != nil {
		t.Error("body class")
	}
}
