package web

import (
	"embed"
	"encoding/base64"
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/database"
	"html/template"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/useragent"
)

//go:embed templates/*.html
var templateFiles embed.FS

type reaction struct{ Character, Title string }

var reactions = []reaction{{"👍", "Thumbs up"}, {"👏", "Clapping"}, {"👋", "Waving hand"}, {"💪", "Muscle"}, {"❤️", "Red heart"}, {"😂", "Face with tears of joy"}, {"🎉", "Party popper"}, {"🔥", "Fire"}}

func avatarPath(secrets *rails.Secrets, id int64, updated time.Time) string {
	path := "/users/" + secrets.SignedID("User", id, "avatar", time.Time{}) + "/avatar"
	if !updated.IsZero() {
		path += "?v=" + updated.UTC().Format("20060102150405")
	}
	return path
}
func epochMillis(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }
func isoTime(t time.Time) string     { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func parseTemplates(secrets *rails.Secrets) (*template.Template, *messageRenderer, error) {
	// The renderer's avatar paths are shared with the templates.
	renderer := newMessageRenderer(secrets)
	t, err := template.New("pages").Funcs(template.FuncMap{
		"quickBoosts": func(clientID string, id int64) template.HTML { return renderer.quickBoosts.render(clientID, id) },
		"helpMailto": func(user database.User) template.HTMLAttr {
			value := "mailto:" + (&mail.Address{Name: user.Name, Address: user.Email}).String()
			return template.HTMLAttr(`href="` + template.HTMLEscapeString(value) + `"`)
		},
		"botCommand": func(origin string, room int64, key string, attachment bool) string {
			prefix := "curl -d 'Hello!' "
			if attachment {
				prefix = "curl -F \"attachment=@/path/to/file\" "
			}
			return prefix + fmt.Sprintf("%s/rooms/%d/%s/messages", origin, room, key)
		},
		"allEmoji": allEmoji,
		"firstName": func(s string) string {
			parts := strings.Fields(s)
			if len(parts) == 0 {
				return ""
			}
			return parts[0]
		},
		"lower": strings.ToLower,
		"agent": useragent.Parse,
		"nextInvolvement": func(kind, value string) string {
			order := []string{"mentions", "everything", "nothing", "invisible"}
			if kind == "Rooms::Direct" {
				order = []string{"everything", "nothing"}
			}
			for i, v := range order {
				if v == value {
					return order[(i+1)%len(order)]
				}
			}
			return order[0]
		},
		"humanInvolvement": func(value string) string {
			return map[string]string{"mentions": "Notifying about @ mentions", "everything": "Notifying about all messages", "nothing": "Notifications are off", "invisible": "Notifications are off and room invisible in sidebar"}[value]
		},
		"asset":       assets.Path,
		"qrpath":      func(value string) string { return "/qr_code/" + base64.URLEncoding.EncodeToString([]byte(value)) },
		"translate":   translationButton,
		"stylesheets": func() template.HTML { return assets.Stylesheets },
		"importmap":   func() template.HTML { return assets.Importmap },
		"avatar": func(id int64, updated ...time.Time) string {
			var version time.Time
			if len(updated) > 0 {
				version = updated[0]
			}
			return renderer.avatar(id, version)
		},
		"versionTime": func(t time.Time) string { return t.UTC().Format("20060102150405") },
		"epoch":       epochMillis,
		"iso":         isoTime,
		"reactions":   func() []reaction { return reactions },
	}).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		return nil, nil, err
	}
	if renderer.quickBoosts, err = compileQuickBoosts(t); err != nil {
		return nil, nil, err
	}
	return t, renderer, nil
}
