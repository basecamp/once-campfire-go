package presentation

import (
	"fmt"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/richtext"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

type APIUser struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Avatar string `json:"avatar_url"`
}

type APIMessage struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"created_at"`
	Body      struct {
		Plain string `json:"plain_text"`
		HTML  string `json:"html"`
	} `json:"body"`
	Creator APIUser `json:"creator"`
	Room    struct {
		ID int64 `json:"id"`
	} `json:"room"`
	URL string `json:"url"`
}

// APIContent owns consumer-specific parsed documents and their display targets.
// It does not compute unused editor, recipient, boost or HTML-fragment variants.
type APIContent struct {
	Records   []database.Message
	Mentioned []int64
	documents []richtext.Document
	targets   map[string]MentionTarget
}

func PrepareAPIContent(records []database.Message) APIContent {
	content := APIContent{Records: append([]database.Message(nil), records...), targets: map[string]MentionTarget{}}
	for _, record := range content.Records {
		doc := richtext.Prepare(record.Body)
		content.documents = append(content.documents, doc)
		for _, token := range doc.Attachables() {
			if _, exists := content.targets[token]; exists {
				continue
			}
			target := MentionTargetFor(token)
			content.targets[token] = target
			if target.ID != 0 {
				content.Mentioned = append(content.Mentioned, target.ID)
			}
		}
	}
	return content
}

func (r *Renderer) APIUser(facts Facts, user database.APIAuthor) APIUser {
	role := "member"
	if user.Role == 1 {
		role = "administrator"
	} else if user.Role == 2 {
		role = "bot"
	}
	avatar := facts.Origin + "/users/" + r.secrets.SignedID("User", user.ID, "avatar", time.Time{}) + "/avatar?v=" + user.UpdatedAt.UTC().Format("20060102150405")
	return APIUser{ID: user.ID, Name: user.Name, Role: role, Avatar: avatar}
}

func (r *Renderer) APIMessages(facts Facts, content APIContent, data database.MessageAPIData) ([]APIMessage, error) {
	resolved := MentionContext(r.secrets, facts.Host, facts.Now, content.targets, data.Mentions)
	result := make([]APIMessage, 0, len(content.Records))
	// Author data and origin are fixed within this response, not across calls.
	var creators map[int64]APIUser
	if len(content.Records) > 1 {
		creators = make(map[int64]APIUser, min(len(content.Records), len(data.Authors)))
	}
	for i, record := range content.Records {
		body, err := content.documents[i].Content(resolved)
		if err != nil {
			return nil, err
		}
		message := APIMessage{ID: record.ID, CreatedAt: record.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"), URL: fmt.Sprintf("%s/rooms/%d/messages/%d", facts.Origin, record.RoomID, record.ID)}
		message.Room.ID = record.RoomID
		message.Body.Plain = body.Plain
		message.Body.HTML = body.BodyHTML
		if strings.TrimSpace(message.Body.Plain) == "" {
			if filename, exists := data.Attachments[record.ID]; exists {
				message.Body.Plain = storage.Filename(filename)
			}
		}
		creator, exists := creators[record.CreatorID]
		if !exists {
			creator = r.APIUser(facts, data.Authors[record.CreatorID])
			if creators != nil {
				creators[record.CreatorID] = creator
			}
		}
		message.Creator = creator
		result = append(result, message)
	}
	return result, nil
}
