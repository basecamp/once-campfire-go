package web

import (
	"github.com/basecamp/once-campfire-go/internal/presentation"
)

func sidebarInput(p page) presentation.SidebarInput {
	return presentation.SidebarInput{User: p.User.Participant(), SidebarRooms: p.SidebarRooms, Placeholders: p.Placeholders, RoomsStream: p.RoomsStream, UserRoomsStream: p.UserRoomsStream, CanCreateRooms: p.CanCreateRooms}
}

func layoutInput(p page) presentation.LayoutInput {
	return presentation.LayoutInput{
		User: presentation.LayoutUser{
			ID:        p.User.ID,
			Name:      p.User.Name,
			Bio:       p.User.Bio,
			UpdatedAt: p.User.UpdatedAt,
			Role:      p.User.Role,
		},
		Room: p.Room, Account: p.Account, Platform: p.Platform,
		Title: p.Title, BodyClass: p.BodyClass, Screen: p.Screen,
		Origin: p.Origin, Stream: p.Stream, VAPIDPublicKey: p.VAPIDPublicKey,
		Notice: p.Notice, Error: p.Error, CustomStyles: p.CustomStyles,
		Frame: p.Frame, Chat: p.Chat, Reload: p.Reload, Invitation: p.Invitation,
	}
}
