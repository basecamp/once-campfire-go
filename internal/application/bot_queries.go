package application

import (
	"context"
	"database/sql"
	"errors"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
)

var ErrNotBot = errors.New("not an active bot")

type BotQueries struct{ DB *database.DB }

func (q *BotQueries) Catalog(ctx context.Context) ([]presentation.BotView, error) {
	bots, err := q.DB.BotDisplays(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(bots))
	for i, bot := range bots {
		ids[i] = bot.ID
	}
	rooms, err := q.DB.BotSharedRooms(ctx, ids)
	if err != nil {
		return nil, err
	}
	views := make([]presentation.BotView, len(bots))
	for i, bot := range bots {
		views[i] = presentation.BotView{User: bot, Rooms: rooms[bot.ID]}
	}
	return views, nil
}

type BotForm struct {
	Bot     database.BotIdentity
	Webhook string
	Avatar  *database.Blob
}

func (q *BotQueries) Form(ctx context.Context, id int64) (BotForm, error) {
	if id == 0 {
		return BotForm{}, nil
	}
	var data BotForm
	var err error
	data.Bot, err = q.DB.BotIdentity(ctx, id)
	if err != nil {
		return BotForm{}, err
	}
	if data.Bot.Role != 2 || data.Bot.Status != 0 {
		return BotForm{}, ErrNotBot
	}
	data.Webhook, err = q.DB.BotWebhook(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BotForm{}, err
	}
	blob, err := q.DB.AttachedBlob(ctx, "User", id, "avatar")
	if err == nil {
		data.Avatar = &blob
	} else if !errors.Is(err, sql.ErrNoRows) {
		return BotForm{}, err
	}
	return data, nil
}
