package presentation

import (
	"fmt"
	"html/template"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/richtext"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

// MessageView contains only message-template inputs, not a partially populated
// persisted model. Database records remain owned by query preparation.
type MessageView struct {
	ID, RoomID, CreatorID             int64
	ClientID, Body, Creator           string
	CreatedAt, UpdatedAt              time.Time
	AllEmoji                          bool
	Fragment                          template.HTML
	Attachment                        *database.Blob
	BlobURL, DownloadURL, PreviewURL  string
	Image                             bool
	Editable                          string
	HTML                              template.HTML
	Permalink, CreatorTitle, RoomName string
	CreatorUpdatedAt                  time.Time
	Boosts                            []database.Boost
}

type Facts struct {
	Host, Origin string
	Now          time.Time
}

func ViewMessages(records []database.Message) []MessageView {
	views := make([]MessageView, len(records))
	for i, m := range records {
		views[i] = ViewMessage(m)
	}
	return views
}

func ViewMessage(m database.Message) MessageView {
	return MessageView{ID: m.ID, RoomID: m.RoomID, CreatorID: m.CreatorID, ClientID: m.ClientID, Body: m.Body, Creator: m.Creator, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

// PreparedMessages owns the bodies and parsed documents used by the association
// query and the presenter. Transformations cannot reload or replace those bodies.
type PreparedMessages struct {
	Records   []database.Message
	Mentioned []int64
	documents []richtext.Document
	targets   map[string]MentionTarget
}

func PrepareMessages(records []database.Message) PreparedMessages {
	p := PreparedMessages{Records: append([]database.Message(nil), records...), documents: make([]richtext.Document, len(records)), targets: map[string]MentionTarget{}}
	for i, m := range records {
		doc := richtext.Prepare(m.Body)
		p.documents[i] = doc
		for _, token := range doc.Attachables() {
			if _, ok := p.targets[token]; ok {
				continue
			}
			target := MentionTargetFor(token)
			p.targets[token] = target
			if target.ID != 0 {
				p.Mentioned = append(p.Mentioned, target.ID)
			}
		}
	}
	return p
}

const UnrenderableMessage template.HTML = `<div class="message message--formatted message--failed center"><div class="message__body"><div class="message__body-content txt-align-center">Failed to load message content</div></div></div>`

func (r *Renderer) Messages(facts Facts, p PreparedMessages, data map[int64]database.MessageDisplay, mentions map[int64]database.UserDisplay) ([]MessageView, error) {
	views, err := r.MessageViews(facts, p, data, mentions)
	if err != nil {
		return nil, err
	}
	for i := range views {
		if views[i].Fragment == UnrenderableMessage {
			continue
		}
		body, err := r.MessageMarkup(views[i])
		if err != nil {
			return nil, err
		}
		views[i].Fragment = template.HTML(body)
	}
	return views, nil
}

// MessageViews prepares complete display inputs without selecting their envelope.
func (r *Renderer) MessageViews(facts Facts, p PreparedMessages, data map[int64]database.MessageDisplay, mentions map[int64]database.UserDisplay) ([]MessageView, error) {
	views := ViewMessages(p.Records)
	rich := MentionContext(r.secrets, facts.Host, facts.Now, p.targets, mentions)
	for i := range views {
		detail := data[views[i].ID]
		if detail.Author == nil {
			views[i].Fragment = UnrenderableMessage
			continue
		}
		author := detail.Author
		views[i].Creator = author.Name
		views[i].CreatorTitle = author.Title()
		views[i].CreatorUpdatedAt = author.UpdatedAt
		views[i].Permalink = MessagePermalink(facts.Origin, views[i].RoomID, views[i].ID)
		views[i].RoomName = DisplayRoom(detail.Room, detail.Participants, database.RoomParticipant{}).Name
		result, err := p.documents[i].Display(rich)
		if err != nil {
			return nil, err
		}
		views[i].HTML = template.HTML(result.Presentation)
		views[i].AllEmoji = allEmoji(result.Plain)
		if sound := soundHTML(result.Plain); sound != "" {
			views[i].HTML = template.HTML(sound)
		}
		views[i].Boosts = detail.Boosts
		if detail.Attachment != nil {
			if err := r.MessageAttachment(&views[i], *detail.Attachment); err != nil {
				return nil, err
			}
		}
	}
	return views, nil
}

func (r *Renderer) MessageAttachment(view *MessageView, blob database.Blob) error {
	view.Attachment = &blob
	view.BlobURL = storage.BlobURL(r.storageVerifier, blob)
	view.DownloadURL = view.BlobURL + "?disposition=attachment"
	view.Image = storage.Variable(blob.Type())
	if view.Image || storage.Previewable(blob.Type()) {
		variation := storage.Resize(1200, 800, "")
		if storage.Previewable(blob.Type()) {
			variation = storage.Variation{{Key: "format", Value: storage.Symbol("webp")}, {Key: "resize_to_limit", Value: []any{int64(1200), int64(800)}}}
		}
		var err error
		view.PreviewURL, err = storage.RepresentationURL(r.storageVerifier, blob, variation)
		if err != nil {
			return err
		}
	}
	view.HTML = template.HTML(attachmentHTML(blob, view.BlobURL, view.DownloadURL, view.PreviewURL))
	return nil
}

func MessagePermalink(origin string, room, message int64) string {
	if origin == "" {
		origin = "http://example.org"
	}
	return fmt.Sprintf("%s/rooms/%d/@%d", origin, room, message)
}
