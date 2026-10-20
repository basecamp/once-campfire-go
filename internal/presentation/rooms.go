package presentation

import (
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
)

type RoomView struct {
	database.Room
	Members     []database.RoomParticipant
	Unread      bool
	Involvement string
}

func (r RoomView) Label() string {
	if len(r.Members) == 1 {
		fields := strings.Fields(r.Members[0].Name)
		if len(fields) > 0 {
			return fields[0]
		}
		return ""
	}
	var names []string
	for _, member := range r.Members {
		var initials strings.Builder
		for i, word := range strings.Fields(member.Name) {
			if i >= 3 {
				break
			}
			initials.WriteString(strings.ToUpper(string([]rune(word)[0])))
		}
		names = append(names, initials.String())
	}
	if len(names) == 2 {
		return names[0] + "+" + names[1]
	}
	if len(names) > 2 {
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
	return strings.Join(names, "")
}

func DisplayRoom(
	room database.Room,
	members []database.RoomParticipant,
	user database.RoomParticipant,
) RoomView {
	view := RoomView{Room: room}
	if room.Type == "Rooms::Direct" {
		var names []string
		for _, member := range members {
			if member.ID != user.ID {
				view.Members = append(view.Members, member)
				names = append(names, member.Name)
			}
		}
		if len(view.Members) == 0 {
			view.Members = []database.RoomParticipant{user}
			view.Name = user.Name
		} else {
			switch len(names) {
			case 1:
				view.Name = names[0]
			case 2:
				view.Name = names[0] + " and " + names[1]
			default:
				view.Name = strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
			}
		}
	}
	return view
}
