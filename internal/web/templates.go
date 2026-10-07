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

type reaction struct {
	Character, Title string
	body             []byte // pre-rendered reaction-body form bytes (see prepareReactionBodies)
}

var reactions = []reaction{
	{"👍", "Thumbs up", nil}, {"👏", "Clapping", nil}, {"👋", "Waving hand", nil}, {"💪", "Muscle", nil},
	{"❤️", "Red heart", nil}, {"😂", "Face with tears of joy", nil}, {"🎉", "Party popper", nil}, {"🔥", "Fire", nil},
}

// reactionBodies is the template-visible form of the pre-rendered reaction
// bodies, built once by prepareReactionBodies and returned by the reactions
// func (which is called once per message render; the slice is not rebuilt per
// call).
var reactionBodies []template.HTML

// prepareReactionBodies renders each fixed reaction's form body once through
// the reaction-body template, so message-actions' {{.}} prints pre-rendered
// template.HTML bodies exactly like upstream main (PR #9) — the message id and
// client id stay in the outer form template with their contextual escaping
// intact. The bytes are stashed on the reactions table and shared by the
// html/template path (the reactions func) and the compiled renderer, which
// appends them raw (htmlescaper is the identity on template.HTML). Idempotent:
// parsing the templates twice recomputes the same bytes.
func prepareReactionBodies(t *template.Template) error {
	for i := range reactions {
		var body strings.Builder
		if err := t.ExecuteTemplate(&body, "reaction-body", &reactions[i]); err != nil {
			return err
		}
		reactions[i].body = []byte(body.String())
	}
	bodies := make([]template.HTML, len(reactions))
	for i := range reactions {
		bodies[i] = template.HTML(reactions[i].body)
	}
	reactionBodies = bodies
	return nil
}

// signedAvatar signs the avatar URL for a user, appending a deterministic
// version stamp when updated is non-zero. Shared by the template funcs (with
// a variadic adapter) and the compiled fragment renderer, so both paths
// produce identical URLs. The hot call sites go through avatarURL's cache.
func signedAvatar(secrets *rails.Secrets, id int64, updated time.Time) string {
	token := secrets.SignedID("User", id, "avatar", time.Time{})
	path := fmt.Sprintf("/users/%s/avatar", token)
	if !updated.IsZero() {
		path += "?v=" + updated.UTC().Format("20060102150405")
	}
	return path
}

func templateFuncs(secrets *rails.Secrets, avatars *avatarCache) template.FuncMap {
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
			return avatarURL(avatars, secrets, id, t)
		},
		"versionTime": func(t time.Time) string { return t.UTC().Format("20060102150405") },
		"epoch":       func(t time.Time) string { return fmt.Sprintf("%d", t.UnixMilli()) },
		"iso":         func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") },
		"reactions":   func() []template.HTML { return reactionBodies },
	}
}

func parseTemplates(secrets *rails.Secrets, avatars *avatarCache) (*template.Template, error) {
	t, err := template.New("pages").Funcs(templateFuncs(secrets, avatars)).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		return nil, err
	}
	// Rendering the reaction bodies executes templates, which marks the set
	// un-clonable; run the pass on a private copy so the served set stays
	// pristine (tests and roomShell Clone it).
	working, err := t.Clone()
	if err != nil {
		return nil, err
	}
	if err := prepareReactionBodies(working); err != nil {
		return nil, err
	}
	return t, nil
}
