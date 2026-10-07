package web

import (
	"embed"
	"encoding/base64"
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/database"
	"html/template"
	"net/mail"
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

// signedAvatar signs the avatar URL for a user, appending a deterministic
// version stamp when updated is non-zero. Shared by the template funcs (with
// a variadic adapter) and the compiled fragment renderer, so both paths
// produce identical URLs.
func signedAvatar(secrets *rails.Secrets, id int64, updated time.Time) string {
	token := secrets.SignedID("User", id, "avatar", time.Time{})
	path := fmt.Sprintf("/users/%s/avatar", token)
	if !updated.IsZero() {
		path += "?v=" + updated.UTC().Format("20060102150405")
	}
	return path
}

func templateFuncs(secrets *rails.Secrets) template.FuncMap {
	return template.FuncMap{
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
			var t time.Time
			if len(updated) > 0 {
				t = updated[0]
			}
			return signedAvatar(secrets, id, t)
		},
		"versionTime": func(t time.Time) string { return t.UTC().Format("20060102150405") },
		"epoch":       func(t time.Time) string { return fmt.Sprintf("%d", t.UnixMilli()) },
		"iso":         func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") },
		"reactions":   func() []reaction { return reactions },
	}
}

func parseTemplates(secrets *rails.Secrets) (*template.Template, error) {
	t, err := template.New("pages").Funcs(templateFuncs(secrets)).ParseFS(templateFiles, "templates/*.html")
	return t, err
}
