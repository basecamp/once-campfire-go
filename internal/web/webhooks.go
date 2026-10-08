package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

// enqueueWebhooks queues webhook deliveries to mentioned bots (or every
// user, for direct rooms). mentioned carries the mentioned user ids the
// create path already derived from the body's single parse (ENGINE-45b);
// nil means "derive here".
func (s *Server) enqueueWebhooks(message database.Message, room database.Room, mentioned []int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var candidates []database.User
	var err error
	if room.Type == "Rooms::Direct" {
		candidates, err = s.DB.Users(ctx, room.ID, true)
	} else {
		if mentioned == nil {
			mentioned = s.mentionedIDs(ctx, message.Body)
		}
		for _, id := range mentioned {
			u, e := s.DB.User(ctx, id)
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			if e != nil {
				err = e
				break
			}
			candidates = append(candidates, u)
		}
	}
	if err != nil {
		slog.Error("webhook recipients failed", "error", err)
		return
	}
	seen := []int64{}
	for _, bot := range candidates {
		if bot.Role != 2 || bot.Status != 0 || bot.ID == message.CreatorID || slices.Contains(seen, bot.ID) {
			continue
		}
		seen = append(seen, bot.ID)
		s.Jobs.Enqueue("webhook", func(ctx context.Context) error { return s.deliverWebhook(ctx, bot.ID, message.ID) })
	}
}
func (s *Server) deliverWebhook(ctx context.Context, botID, messageID int64) error {
	bot, err := s.DB.User(ctx, botID)
	if err != nil {
		return err
	}
	// A bot mentioned outside its rooms may still receive a webhook. The payload is
	// selected by the authenticated posting action, not by the bot's memberships.
	message, err := s.DB.Message(ctx, messageID)
	if err != nil {
		return err
	}
	creatorID := message.CreatorID
	room, err := s.DB.FindRoom(ctx, message.RoomID)
	if err != nil {
		return err
	}
	var endpoint sql.NullString
	err = s.DB.Read.QueryRowContext(ctx, "SELECT url FROM webhooks WHERE user_id=? LIMIT 1", bot.ID).Scan(&endpoint)
	if err != nil {
		return err
	}
	plain := s.plainText(ctx, message.Body)
	if strings.TrimSpace(plain) == "" {
		if blob, e := s.Storage.Attached(ctx, "Message", message.ID, "attachment"); e == nil {
			plain = blob.Filename
		}
	}
	plain = strings.TrimSpace(strings.ReplaceAll(plain, "@"+bot.Name, ""))
	var rawBody *string
	if err := s.DB.Read.QueryRowContext(ctx, "SELECT body FROM action_text_rich_texts WHERE record_type='Message' AND record_id=? AND name='body' LIMIT 1", messageID).Scan(&rawBody); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var roomName *string
	if room.Name != "" {
		roomName = &room.Name
	}
	payload := struct {
		User struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
		Room struct {
			ID   int64   `json:"id"`
			Name *string `json:"name"`
			Path string  `json:"path"`
		} `json:"room"`
		Message struct {
			ID   int64 `json:"id"`
			Body struct {
				HTML  *string `json:"html"`
				Plain string  `json:"plain"`
			} `json:"body"`
			Path string `json:"path"`
		} `json:"message"`
	}{}
	payload.User.ID = creatorID
	payload.User.Name = message.Creator
	payload.Room.ID = room.ID
	payload.Room.Name = roomName
	payload.Room.Path = fmt.Sprintf("/rooms/%d/%s/messages", room.ID, bot.BotKey())
	payload.Message.ID = message.ID
	payload.Message.Body.HTML = rawBody
	payload.Message.Body.Plain = plain
	payload.Message.Path = fmt.Sprintf("/rooms/%d/@%d", room.ID, message.ID)
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	raw, err = rails.CanonicalJSON(raw, true)
	if err != nil {
		return err
	}
	reply, err := s.Webhooks.Deliver(ctx, endpoint.String, raw)
	if err != nil {
		return err
	}
	if reply.Text == nil && reply.Filename == "" {
		return nil
	}
	var staged *storage.Staged
	if reply.Text == nil {
		contentType := storage.Identify(reply.Attachment, reply.Filename, reply.ContentType)
		staged, err = s.Storage.StageFile(ctx, reply.Filename, contentType, bytes.NewReader(reply.Attachment))
		if err != nil {
			return err
		}
	}
	created, err := s.saveNewMessage(ctx, bot.ID, room.ID, "", reply.Text, staged, true)
	if err != nil {
		return err
	}
	s.messageCreated(created, room, nil)
	views, err := s.messageViews(ctx, []database.Message{created})
	if err != nil {
		return err
	}
	markup, err := s.markup("message", views[0])
	if err != nil {
		return err
	}
	s.publish(room.ID, stream("append", room.DOM("messages"), markup))
	return nil
}
