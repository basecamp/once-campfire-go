package views

import "testing"

func TestSidebarDirectInitialsLikeTheReference(t *testing.T) {
	direct := SidebarDirect{Members: []UserSummary{{Name: "jason  fried"}, {Name: "Kevin"}}}
	if got := direct.MemberInitials(); got != "JF+K" {
		t.Errorf("got %s", got)
	}
	direct.Members = append(direct.Members, UserSummary{Name: "Jorge Manrubia de la Cruz"})
	if got := direct.MemberInitials(); got != "JF, K, and JMD" {
		t.Errorf("got %s", got)
	}
	if got := (&UserSummary{Name: "\tAnn\vB"}).FirstName(); got != "Ann\vB" {
		t.Errorf("got %q", got)
	}
}

func TestCachesAFragmentPerRecordVersion(t *testing.T) {
	ctx := testContext()
	ctx.Cache = NewFragmentCache(DefaultMaxBytes)
	message := &MessageView{ID: 1, ClientMessageID: "c", Content: MessageText{}}
	first := CachedMessage(ctx, message)
	if CachedMessage(ctx, message) != first || CachedMessageFragment(ctx.Cache, 1, message.UpdatedAt) != first {
		t.Error("not cached")
	}
	if CachedMessageItem(ctx, MessageItemView(message)) != first {
		t.Error("item not cached")
	}
	message.UpdatedAt = message.UpdatedAt.Add(1000)
	if CachedMessageFragment(ctx.Cache, 1, message.UpdatedAt) != nil || CachedMessage(ctx, message) == first {
		t.Error("a new version reused the old fragment")
	}
	if CachedMessageFragment(nil, 1, message.UpdatedAt) != nil {
		t.Error("nil cache")
	}
	item := MessageItemFragment("c", 9, first)
	if CachedMessageItem(ctx, item) != first || item.DomID("edit") != "edit_message_c" || item.RoomID() != 9 {
		t.Error("fragment item")
	}
}
