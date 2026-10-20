package presentation

import "github.com/basecamp/once-campfire-go/internal/database"

type BotView struct {
	User  database.BotDisplay
	Rooms []database.SharedRoom
}
