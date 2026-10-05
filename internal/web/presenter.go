package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"github.com/basecamp/once-campfire-go/internal/views"
)

// presenter is the reference's Presenter (reference/crates/campfire/src/controllers/presenters.rs):
// the message views a page of messages needs, with the users and rooms it looks up along the way
// remembered, as Rails preloads them.
type presenter struct {
	s   *Server
	ctx context.Context
	// Current.request_host, which opengraph embeds are checked against; "" outside a request.
	requestHost string
	users       map[int64]database.User
	roomNames   map[int64]string
}

func (s *Server) newPresenter(ctx context.Context, requestHost string) *presenter {
	return &presenter{s: s, ctx: ctx, requestHost: requestHost, users: map[int64]database.User{}, roomNames: map[int64]string{}}
}

// requestHost is request.host: the Host without its port.
func (s *Server) requestHost(r *http.Request) string {
	_, host, _ := strings.Cut(s.origin(r), "://")
	if name, _, found := strings.Cut(host, ":"); found && !strings.HasPrefix(host, "[") {
		return name
	}
	return host
}

// roomKind is the view's RoomKind for a room's STI type.
func roomKind(roomType string) views.RoomKind {
	switch roomType {
	case "Rooms::Closed":
		return views.RoomKindClosed
	case "Rooms::Direct":
		return views.RoomKindDirect
	}
	return views.RoomKindOpen
}

// userView is user_view: what the message views show of a user.
func (s *Server) userView(u *database.User) views.UserView {
	return views.UserView{ID: u.ID, Name: u.Name, Title: u.Title(), AvatarURL: s.avatarPath(u.ID, u.UpdatedAt)}
}

// referenceRichContext is DbResolver's render context: mentions resolve to users through their
// signed (or, for users only, unverified) GlobalIDs, one User::find_by_id per lookup.
func (s *Server) referenceRichContext(ctx context.Context, host string) richtext.Context {
	return richtext.Context{Host: host, Resolve: func(token string, verified bool) (*richtext.Mention, error) {
		var gid string
		var err error
		if verified {
			gid, err = s.Secrets.VerifySGID(token, "attachable", s.DB.Now())
			if err != nil {
				return nil, nil
			}
		} else {
			gid, err = rails.UnverifiedUserGID(token)
			if err != nil {
				return nil, err
			}
		}
		gid, _, _ = strings.Cut(gid, "?")
		parts := strings.Split(gid, "/")
		if len(parts) != 5 || parts[3] != "User" {
			return nil, nil
		}
		id, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil {
			return nil, nil
		}
		u, found, err := s.DB.UserFindByID(ctx, id)
		if err != nil || !found {
			return nil, err
		}
		m := s.mention(u)
		return &m, nil
	}}
}

// richContext is the presenter's render context, for the request's host.
func (p *presenter) richContext() richtext.Context {
	return p.s.referenceRichContext(p.ctx, p.requestHost)
}

func (p *presenter) user(id int64) (database.User, error) {
	if u, ok := p.users[id]; ok {
		return u, nil
	}
	u, found, err := p.s.DB.UserFindByID(p.ctx, id)
	if err != nil {
		return u, err
	}
	if !found {
		return u, database.ErrNoRows
	}
	p.users[id] = u
	return u, nil
}

func (p *presenter) userView(id int64) (views.UserView, error) {
	u, err := p.user(id)
	if err != nil {
		return views.UserView{}, err
	}
	return p.s.userView(&u), nil
}

func optionalName(name database.NullString) *string {
	if !name.Valid {
		return nil
	}
	return &name.String
}

// roomDisplayName is room_display_name(room, for_user:).
func (p *presenter) roomDisplayName(room *database.ReferenceRoom, forUser *database.User) (string, error) {
	direct := room.Type == "Rooms::Direct"
	var names []string
	if direct {
		users, err := p.s.DB.RoomUsers(p.ctx, room.ID)
		if err != nil {
			return "", err
		}
		for _, u := range users {
			if forUser == nil || forUser.ID != u.ID {
				names = append(names, u.Name)
			}
		}
	}
	var forUserName *string
	if forUser != nil {
		forUserName = &forUser.Name
	}
	return views.RoomDisplayName(optionalName(room.Name), direct, names, forUserName), nil
}

func (p *presenter) roomView(room *database.ReferenceRoom, forUser *database.User) (views.RoomView, error) {
	name, err := p.roomDisplayName(room, forUser)
	return views.RoomView{ID: room.ID, Kind: roomKind(room.Type), Name: optionalName(room.Name), DisplayName: name}, err
}

// roomName is room_and_name: message.room with room_display_name(message.room, for_user: nil).
func (p *presenter) roomName(roomID int64) (string, error) {
	if name, ok := p.roomNames[roomID]; ok {
		return name, nil
	}
	room, found, err := p.s.DB.RoomFindByID(p.ctx, roomID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", database.ErrNoRows
	}
	name, err := p.roomDisplayName(&room, nil)
	if err != nil {
		return "", err
	}
	p.roomNames[roomID] = name
	return name, nil
}

// plainTextBody is Message#plain_text_body: body.to_plain_text.presence || attachment&.filename || "",
// with the models' rich text (no request host).
func (p *presenter) plainTextBody(m *database.ReferenceMessage) (string, error) {
	body, err := p.s.DB.MessageBodyHTML(p.ctx, m.ID)
	if err != nil {
		return "", err
	}
	if body != nil {
		text, err := richtext.PlainText(*body, p.s.referenceRichContext(p.ctx, ""))
		if err != nil {
			text = ""
		}
		if strings.TrimSpace(text) != "" {
			return text, nil
		}
	}
	filename, _, err := p.s.DB.MessageAttachmentFilename(p.ctx, m.ID)
	return filename, err
}

// messages is `render @messages`: each message's cached fragment when the store has its version,
// else its view.
func (p *presenter) messages(messages []database.ReferenceMessage) ([]views.MessageItem, error) {
	items := make([]views.MessageItem, len(messages))
	for i := range messages {
		item, err := p.messageItem(&messages[i])
		if err != nil {
			return nil, err
		}
		items[i] = item
	}
	return items, nil
}

// messageItem is `render message`, as messages does it.
func (p *presenter) messageItem(m *database.ReferenceMessage) (views.MessageItem, error) {
	if f := views.CachedMessageFragment(p.s.views, m.ID, m.UpdatedAt); f != nil {
		return views.MessageItemFragment(m.ClientMessageID, m.RoomID, f), nil
	}
	view, err := p.message(m)
	if err != nil {
		return views.MessageItem{}, err
	}
	return views.MessageItemView(view), nil
}

// message is a message as messages/_message shows it. message_tag rescues what its block raises
// (a creator that's gone, say) and renders messages/_unrenderable instead.
func (p *presenter) message(m *database.ReferenceMessage) (*views.MessageView, error) {
	roomName, err := p.roomName(m.RoomID)
	if err != nil {
		return nil, err
	}
	view, err := p.renderableMessage(m, roomName)
	if err == database.ErrNoRows {
		return &views.MessageView{ID: m.ID, ClientMessageID: m.ClientMessageID, RoomID: m.RoomID, RoomName: roomName,
			Creator: views.UserView{ID: m.CreatorID}, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, Content: views.MessageUnrenderable{}}, nil
	}
	return view, err
}

func (p *presenter) renderableMessage(m *database.ReferenceMessage, roomName string) (*views.MessageView, error) {
	plainText, err := p.plainTextBody(m)
	if err != nil {
		return nil, err
	}
	creator, err := p.userView(m.CreatorID)
	if err != nil {
		return nil, err
	}
	content, err := p.content(m, plainText)
	if err != nil {
		return nil, err
	}
	boosts, err := p.boosts(m.ID)
	if err != nil {
		return nil, err
	}
	return &views.MessageView{ID: m.ID, ClientMessageID: m.ClientMessageID, RoomID: m.RoomID, RoomName: roomName, Creator: creator,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, AllEmoji: allEmoji(plainText), Content: content, Boosts: boosts}, nil
}

// boosts is message.boosts.ordered.
func (p *presenter) boosts(message int64) ([]views.BoostView, error) {
	boosts, err := p.s.DB.BoostsForMessageOrdered(p.ctx, message)
	if err != nil {
		return nil, err
	}
	result := make([]views.BoostView, 0, len(boosts))
	for i := range boosts {
		view, err := p.boost(&boosts[i])
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}

func (p *presenter) boost(b *database.ReferenceBoost) (views.BoostView, error) {
	booster, err := p.userView(b.BoosterID)
	return views.BoostView{ID: b.ID, UpdatedAt: b.UpdatedAt, MessageID: b.MessageID, Content: b.Content, AllEmoji: allEmoji(b.Content), Booster: booster}, err
}

// content is message.content_type, with what message_presentation shows for it.
func (p *presenter) content(m *database.ReferenceMessage, plainText string) (views.MessageContent, error) {
	body, err := p.s.DB.MessageBodyHTML(p.ctx, m.ID)
	if err != nil {
		return nil, err
	}
	html := ""
	if body != nil {
		html = *body
	}
	rich := p.richContext()
	// message_tag evaluates message.plain_text_body first and renders messages/_unrenderable
	// where that raises.
	if _, err := richtext.PlainText(html, rich); err != nil {
		return views.MessageUnrenderable{}, nil
	}
	attachment, err := p.attachment(m)
	if err != nil || attachment != nil {
		return attachment, err
	}
	if sound := soundIn(plainText); sound != nil {
		return sound, nil
	}
	result, _ := richtext.Display(html, rich)
	return views.MessageText{HTML: result.Presentation}, nil
}

// soundIn is plain_text_body.match(/\A\/play (?<name>\w+)\z/) then Sound.find_by_name.
func soundIn(plainText string) *views.SoundView {
	name, found := strings.CutPrefix(plainText, "/play ")
	if !found || name == "" {
		return nil
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return nil
		}
	}
	s, ok := sounds[name]
	if !ok {
		return nil
	}
	view := &views.SoundView{URL: assets.Path(name + ".mp3")}
	if s.Image != "" {
		view.Image = &views.SoundImage{Src: assets.Path("sounds/" + s.Image), Width: uint32(s.Width), Height: uint32(s.Height)}
	} else {
		view.Text = &s.Text
	}
	return view
}

// attachment is message.attachment as Messages::AttachmentPresentation needs it.
func (p *presenter) attachment(m *database.ReferenceMessage) (*views.AttachmentView, error) {
	attached, found, err := p.s.DB.AttachedBlob(p.ctx, "Message", m.ID, "attachment")
	if err != nil || !found {
		return nil, err
	}
	blob := storageBlob(attached)
	var preview views.AttachmentPreview = views.AttachmentFile{}
	if storage.Previewable(blob.Type()) || storage.Variable(blob.Type()) {
		if strings.HasPrefix(blob.Type(), "video") {
			poster := storage.Variation{{Key: "format", Value: storage.Symbol("webp")}, {Key: "resize_to_limit", Value: []any{int64(1200), int64(800)}}}
			url, err := p.s.Storage.RepresentationURL(blob, poster)
			if err != nil {
				return nil, err
			}
			preview = views.AttachmentVideo{PosterURL: url}
		} else {
			url, err := p.s.Storage.RepresentationURL(blob, storage.Resize(1200, 800, ""))
			if err != nil {
				return nil, err
			}
			preview = views.AttachmentImage{ThumbURL: url}
		}
	}
	blobPath := p.s.Storage.BlobURL(blob)
	width, height := blobDimension(blob.Metadata, "width"), blobDimension(blob.Metadata, "height")
	return &views.AttachmentView{Filename: blob.Filename, BlobPath: blobPath, DownloadPath: blobPath + "?disposition=attachment",
		Preview: preview, Width: width, Height: height}, nil
}

// storageBlob is a blob row as the storage package handles it.
func storageBlob(b database.Blob) storage.Blob {
	blob := storage.Blob{ID: b.ID, Key: b.Key, Filename: b.Filename, Metadata: json.RawMessage(b.Metadata.String),
		ServiceName: b.ServiceName, ByteSize: b.ByteSize, Checksum: b.Checksum.String, CreatedAt: b.CreatedAt}
	if b.ContentType.Valid {
		blob.ContentType = &b.ContentType.String
	}
	if !json.Valid(blob.Metadata) {
		blob.Metadata = json.RawMessage("{}")
	}
	return blob
}

// blobDimension is attachment.metadata[name]: an Integer for images, a Float for videos.
func blobDimension(metadata json.RawMessage, name string) *views.RubyNumber {
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.UseNumber()
	var values map[string]any
	if decoder.Decode(&values) != nil {
		return nil
	}
	number, ok := values[name].(json.Number)
	if !ok {
		return nil
	}
	if !strings.ContainsAny(string(number), ".eE") {
		if i, err := number.Int64(); err == nil {
			return views.Ptr(views.RubyInt(i))
		}
	}
	f, err := number.Float64()
	if err != nil {
		return nil
	}
	return views.Ptr(views.RubyFloat(f))
}

// editableBody is editable_body(message) as the editor's value.
func (p *presenter) editableBody(m *database.ReferenceMessage) (string, error) {
	body, err := p.s.DB.MessageBodyHTML(p.ctx, m.ID)
	if err != nil {
		return "", err
	}
	html := ""
	if body != nil {
		html = *body
	}
	return richtext.Editable(html, p.richContext())
}
