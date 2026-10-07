package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestPreparedReactionBodiesKeepFormBytesAndEscaping(t *testing.T) {
	app, _, _, _ := testApp(t)
	// The former dynamic loop is an independent byte/escaping reference. IDs
	// belong to each form; only the fixed inner contents can be pre-rendered.
	legacy := template.Must(template.New("legacy").Funcs(template.FuncMap{
		"reactions": func() []reaction { return reactions },
	}).Parse(`{{$m := .}}{{range reactions}}<form data-turbo-frame="boosting_message_{{$m.ClientID}}" data-action="popup#close" action="/messages/{{$m.ID}}/boosts" accept-charset="UTF-8" method="post"><input type="hidden" id="boost_content" name="boost[content]" value="{{.Character}}"><button name="button" type="submit" title="{{.Title}}" class="btn message__action-btn" data-emoji="{{.Character}}"><figure class="margin-none boost-character">{{.Character}}</figure><span class="for-screen-reader">{{.Title}}</span></button></form>{{end}}`))
	for _, message := range []database.Message{
		{ID: 0, ClientID: ""},
		{ID: 9223372036854775807, ClientID: `quoted" & <client>`},
		{ID: -1, ClientID: "next-client"},
		{ID: 9223372036854775807, ClientID: `quoted" & <client>`},
	} {
		var expected, actual bytes.Buffer
		if err := legacy.Execute(&expected, message); err != nil {
			t.Fatal(err)
		}
		if err := app.templates.ExecuteTemplate(&actual, "message-actions", messageView{Message: message}); err != nil {
			t.Fatal(err)
		}
		_, forms, found := strings.Cut(actual.String(), `<div class="quick-boosts">`)
		if !found {
			t.Fatal("missing reactions")
		}
		forms, _, found = strings.Cut(forms, `<a class="btn message__action-btn message__boost-btn"`)
		if !found || forms != expected.String() {
			t.Fatalf("prepared forms changed bytes/escaping: got %q, want %q", forms, expected.String())
		}
	}
}
