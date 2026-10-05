package web

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

// Pre-render the fixed message markup with slots for its scalar values. The
// original html/template still handles reactions, branches and asset paths at
// startup. Messages with boosts retain the general template execution path.
type messagePart struct {
	text string
	slot int
}

type messageTemplates [4][]messagePart

// Fall back automatically if a future template edit introduces an unknown slot.
const messageTemplateSHA256 = "6ecd6e2abd10eea8d28f591cd4288c0a34cc24fa9ff3c061c58e35c6be904b60"

var messageSlots = [][]string{
	{".ID", "$m.ID"}, {".RoomID"}, {".CreatorID"}, {".ClientID", "$m.ClientID"},
	{".CreatorTitle"}, {".Creator"}, {".RoomName"},
	{"epoch .CreatedAt"}, {"epoch .UpdatedAt"}, {"iso .CreatedAt"},
	{"avatar .CreatorID .CreatorUpdatedAt"}, {".Permalink"}, {".HTML"},
	{".DownloadURL"}, {".BlobURL"}, {".Attachment.Filename"},
}

func compileMessageTemplates(original *template.Template) (messageTemplates, error) {
	var layouts messageTemplates
	source, err := templateFiles.ReadFile("templates/messages.html")
	if err != nil {
		return layouts, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(source)) != messageTemplateSHA256 {
		return layouts, nil
	}
	marker := "campfire-native-" + rand.Text() + "-"
	var replacements []string
	for i, expressions := range messageSlots {
		for _, expression := range expressions {
			replacements = append(replacements, "{{"+expression+"}}", "{{"+strconv.Quote(marker+strconv.Itoa(i)+"-end")+"}}")
		}
	}
	source = []byte(strings.NewReplacer(replacements...).Replace(string(source)))
	for i := range layouts {
		t, err := original.Clone()
		if err != nil {
			return layouts, err
		}
		if _, err = t.Parse(string(source)); err != nil {
			return layouts, err
		}
		v := messageView{AllEmoji: i&1 != 0}
		if i&2 != 0 {
			v.Attachment = &storage.Blob{}
		}
		var b strings.Builder
		if err := t.ExecuteTemplate(&b, "message-uncached", v); err != nil {
			return layouts, err
		}
		raw := b.String()
		for {
			before, after, ok := strings.Cut(raw, marker)
			if !ok {
				layouts[i] = append(layouts[i], messagePart{text: raw, slot: -1})
				break
			}
			number, rest, ok := strings.Cut(after, "-end")
			slot, err := strconv.Atoi(number)
			if !ok || err != nil || slot < 0 || slot >= len(messageSlots) {
				return layouts, fmt.Errorf("invalid message template slot %q", number)
			}
			layouts[i] = append(layouts[i], messagePart{text: before, slot: slot})
			raw = rest
		}
	}
	return layouts, nil
}

// Keep the standard library's URL scheme filtering, normalization and attribute
// escaping for fields that can include an application origin or a download URL.
var messageURLTemplate = template.Must(template.New("url").Parse(`<a href="{{.}}"></a>`))

func messageURL(value string) (string, error) {
	var b strings.Builder
	if err := messageURLTemplate.Execute(&b, value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimPrefix(b.String(), `<a href="`), `"></a>`), nil
}

func messageEscape(value string) string {
	return template.HTMLEscapeString(strings.ReplaceAll(value, "\x00", "\ufffd"))
}

func avatarPath(secrets *rails.Secrets, id int64, updated time.Time) string {
	path := "/users/" + secrets.SignedID("User", id, "avatar", time.Time{}) + "/avatar"
	if !updated.IsZero() {
		path += "?v=" + updated.UTC().Format("20060102150405")
	}
	return path
}

func (s *Server) messageMarkup(v messageView) (string, error) {
	if len(v.Boosts) > 0 || len(s.messageTemplates[0]) == 0 {
		return s.markup("message-uncached", v)
	}
	var err error
	values := [16]string{
		strconv.FormatInt(v.ID, 10), strconv.FormatInt(v.RoomID, 10), strconv.FormatInt(v.CreatorID, 10),
		messageEscape(v.ClientID), messageEscape(v.CreatorTitle), messageEscape(v.Creator), messageEscape(v.RoomName),
		strconv.FormatInt(v.CreatedAt.UnixMilli(), 10), strconv.FormatInt(v.UpdatedAt.UnixMilli(), 10),
		v.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"), avatarPath(s.Secrets, v.CreatorID, v.CreatorUpdatedAt),
		messageEscape(v.Permalink), string(v.HTML),
	}
	index := 0
	if v.AllEmoji {
		index |= 1
	}
	if v.Attachment != nil {
		index |= 2
		values[13], err = messageURL(v.DownloadURL)
		if err != nil {
			return "", err
		}
		values[14] = messageEscape(v.BlobURL)
		values[15] = messageEscape(v.Attachment.Filename)
	}
	var b strings.Builder
	size := 0
	for _, part := range s.messageTemplates[index] {
		size += len(part.text)
		if part.slot >= 0 {
			size += len(values[part.slot])
		}
	}
	b.Grow(size)
	for _, part := range s.messageTemplates[index] {
		b.WriteString(part.text)
		if part.slot >= 0 {
			b.WriteString(values[part.slot])
		}
	}
	return b.String(), nil
}
