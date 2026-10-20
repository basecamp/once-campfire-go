package presentation

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// Adapted from Nick Potts' template-slot renderer in upstream PR #6:
// https://github.com/basecamp/once-campfire-go/pull/6
// Templates remain the markup owner. Only the four no-boost branches are
// compiled; boosts and unsupported source changes use ordinary template execution.
type messagePart struct {
	text string
	slot int
}

type messageLayouts [4][]messagePart

// The slots and their escaping contexts below apply only to this exact source.
const messageTemplateSHA256 = "a3ca5ea00ed8ca4602e2ecfc462b768dcc51881c5e6324185f4bc5cd21c450db"

var messageSlots = [][]string{
	{".ID", "$m.ID"}, {".RoomID"}, {".CreatorID"}, {".ClientID", "$m.ClientID"},
	{".CreatorTitle"}, {".Creator"}, {".RoomName"},
	{"epoch .CreatedAt"}, {"epoch .UpdatedAt"}, {"iso .CreatedAt"},
	{"avatar .CreatorID .CreatorUpdatedAt"}, {".Permalink"}, {".HTML"},
	{".DownloadURL"}, {".BlobURL"}, {".Attachment.Filename"},
}

func compileMessageLayouts(original *template.Template, source []byte) (messageLayouts, error) {
	var layouts messageLayouts
	if fmt.Sprintf("%x", sha256.Sum256(source)) != messageTemplateSHA256 {
		return layouts, nil
	}
	marker := "campfire-message-" + rand.Text() + "-"
	var replacements []string
	for i, expressions := range messageSlots {
		for _, expression := range expressions {
			replacements = append(replacements, "{{"+expression+"}}", "{{"+strconv.Quote(marker+strconv.Itoa(i)+"-end")+"}}")
		}
	}
	compiledSource := strings.NewReplacer(replacements...).Replace(string(source))
	for i := range layouts {
		t, err := original.Clone()
		if err != nil {
			return layouts, err
		}
		if _, err = t.Parse(compiledSource); err != nil {
			return layouts, err
		}
		v := MessageView{AllEmoji: i&1 != 0}
		if i&2 != 0 {
			v.Attachment = &database.Blob{}
		}
		var b strings.Builder
		if err := t.ExecuteTemplate(&b, "message-uncached", v); err != nil {
			return layouts, err
		}
		raw := b.String()
		for {
			before, after, found := strings.Cut(raw, marker)
			if !found {
				layouts[i] = append(layouts[i], messagePart{text: raw, slot: -1})
				break
			}
			number, rest, found := strings.Cut(after, "-end")
			slot, err := strconv.Atoi(number)
			if !found || err != nil || slot < 0 || slot >= len(messageSlots) {
				return layouts, fmt.Errorf("invalid message template slot %q", number)
			}
			layouts[i] = append(layouts[i], messagePart{text: before, slot: slot})
			raw = rest
		}
	}
	return layouts, nil
}

// Ordinary strings in HTML text and quoted non-URL attributes use these exact
// html/template replacements, including plus and NUL. Replacer preserves invalid
// UTF-8 bytes, just as the standard escaper does.
var messageEscaper = strings.NewReplacer(
	"\x00", "\ufffd", "\"", "&#34;", "&", "&amp;", "'", "&#39;",
	"+", "&#43;", "<", "&lt;", ">", "&gt;",
)

// URL scheme filtering and URL normalization remain owned by html/template.
// Permalink and BlobURL are data-* strings, not URL attributes.
var messageURLTemplate = template.Must(template.New("url").Parse(`<a href="{{.}}"></a>`))

func messageURL(value string) (string, error) {
	var b strings.Builder
	if err := messageURLTemplate.Execute(&b, value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimPrefix(b.String(), `<a href="`), `"></a>`), nil
}

func avatarPath(secrets *rails.Secrets, id int64, updated time.Time) string {
	path := "/users/" + secrets.SignedID("User", id, "avatar", time.Time{}) + "/avatar"
	if !updated.IsZero() {
		path += "?v=" + updated.UTC().Format("20060102150405")
	}
	return path
}

func (s *Renderer) MessageMarkup(v MessageView) (string, error) {
	return s.messageMarkup(v, "", "")
}

func (s *Renderer) AppendMessage(target string, v MessageView) (string, error) {
	opening, closing := rails.TurboStreamEnvelope("append", target)
	return s.messageMarkup(v, opening, closing)
}

func (s *Renderer) messageMarkup(v MessageView, opening, closing string) (string, error) {
	if len(v.Boosts) > 0 || len(s.messageLayouts[0]) == 0 {
		body, err := s.Markup("message-uncached", v)
		if err != nil {
			return "", err
		}
		return opening + body + closing, nil
	}
	values := [16]string{
		strconv.FormatInt(v.ID, 10), strconv.FormatInt(v.RoomID, 10), strconv.FormatInt(v.CreatorID, 10),
		messageEscaper.Replace(v.ClientID), messageEscaper.Replace(v.CreatorTitle),
		messageEscaper.Replace(v.Creator), messageEscaper.Replace(v.RoomName),
		strconv.FormatInt(v.CreatedAt.UnixMilli(), 10), strconv.FormatInt(v.UpdatedAt.UnixMilli(), 10),
		v.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"), avatarPath(s.secrets, v.CreatorID, v.CreatorUpdatedAt),
		messageEscaper.Replace(v.Permalink), string(v.HTML),
	}
	index := 0
	if v.AllEmoji {
		index |= 1
	}
	if v.Attachment != nil {
		index |= 2
		var err error
		values[13], err = messageURL(v.DownloadURL)
		if err != nil {
			return "", err
		}
		values[14] = messageEscaper.Replace(v.BlobURL)
		values[15] = messageEscaper.Replace(v.Attachment.Filename)
	}
	var b strings.Builder
	size := len(opening) + len(closing)
	for _, part := range s.messageLayouts[index] {
		size += len(part.text)
		if part.slot >= 0 {
			size += len(values[part.slot])
		}
	}
	b.Grow(size)
	b.WriteString(opening)
	for _, part := range s.messageLayouts[index] {
		b.WriteString(part.text)
		if part.slot >= 0 {
			b.WriteString(values[part.slot])
		}
	}
	b.WriteString(closing)
	return b.String(), nil
}
