package views

import "strconv"

// The parts of RoomsHelper and Rooms::InvolvementsHelper the sidebar and the profile's memberships
// use.

// LinkToRoom is link_to_room(room, **attributes) { content }. options is the attribute hash in
// Ruby order, data-* entries included where the data: key was.
func LinkToRoom(content HTML, roomID int64, options *Attrs) HTML {
	defaults := []attrEntry{
		{"data-rooms-list-target", textAttr("room")},
		{"data-room-id", textAttr(strconv.FormatInt(roomID, 10))},
		{"data-badge-dot-target", textAttr("unread")},
		{"data-sorted-list-target", textAttr("item")},
	}
	return ContentTag("a", linkOptions(RouteRoom(roomID), options.withDefaultData(defaults)), content)
}

// HumanizeInvolvement is HUMANIZE_INVOLVEMENT.
func HumanizeInvolvement(involvement string) string {
	switch involvement {
	case "mentions":
		return "Notifying about @ mentions"
	case "everything":
		return "Notifying about all messages"
	case "nothing":
		return "Notifications are off"
	case "invisible":
		return "Notifications are off and room invisible in sidebar"
	}
	return ""
}

// NextInvolvement is next_involvement_for(room, involvement:).
func NextInvolvement(direct bool, involvement string) string {
	order := []string{"mentions", "everything", "nothing", "invisible"}
	if direct {
		order = []string{"everything", "nothing"}
	}
	for i, candidate := range order {
		if candidate == involvement && i+1 < len(order) {
			return order[i+1]
		}
	}
	return order[0]
}

// InvolvementRoom is a room as the involvement helpers see it.
type InvolvementRoom struct {
	ID int64
	// model_name.param_key of the room's class: "rooms_open", "rooms_closed" or "rooms_direct".
	ParamKey string
	Direct   bool
}

// ButtonToChangeInvolvement is button_to_change_involvement(room, involvement).
func ButtonToChangeInvolvement(ctx *ViewContext, room InvolvementRoom, involvement string) HTML {
	labelID := DomID(room.ParamKey, room.ID, "involvement_label")
	url := WithQuery(RouteRoomInvolvement(room.ID), ParamOne("involvement", NextInvolvement(room.Direct, involvement)))
	content := ImageTag(ctx, "notification-bell-"+involvement+".svg", NewAttrs().AriaHidden().Size(20)) +
		ContentTagText("span", NewAttrs().Class("for-screen-reader").ID(labelID), HumanizeInvolvement(involvement))
	options := NewAttrs().
		Method("put").
		Role("checkbox").
		Aria("checked", true).
		Aria("labelledby", labelID).
		Tabindex(0).
		Class("btn " + involvement)
	return ButtonTo(url, options, content)
}
