package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/httpcompat"
	"github.com/basecamp/once-campfire-go/internal/richtext"
)

func (s *Server) autocomplete(w http.ResponseWriter, r *http.Request, u database.User) {
	var room *int64
	if raw := r.Form.Get("room_id"); strings.TrimSpace(raw) != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		room = &id
	}
	query := r.Form.Get("filter")
	if strings.TrimSpace(query) == "" {
		query = r.Form.Get("query")
	}
	if strings.TrimSpace(query) == "" {
		query = ""
	}
	number, _ := strconv.ParseInt(r.Form.Get("page"), 10, 64)
	data, err := s.AccountQueries.Suggest(r.Context(), u.ID, room, query, number)
	if err != nil {
		s.fail(w, err)
		return
	}
	format := respondFormat(w, r, "html", "json")
	if format == "" {
		return
	}
	if format == "json" {
		w.Header().Set("X-Total-Count", strconv.Itoa(data.Count))
		if data.NextPage != 0 {
			next := *r.URL
			q := next.Query()
			q.Set("page", strconv.FormatInt(data.NextPage, 10))
			next.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
			w.Header().Set("Link", fmt.Sprintf("<%s%s>; rel=\"next\"", s.origin(r), next.String()))
		}
		type suggestion struct {
			Name      string `json:"name"`
			Value     int64  `json:"value"`
			AvatarURL string `json:"avatar_url"`
			SGID      string `json:"sgid"`
		}
		out := []suggestion{}
		for _, m := range data.Mentions {
			out = append(out, suggestion{template.HTMLEscapeString(m.Name), m.ID, s.origin(r) + m.Avatar, m.SGID})
		}
		writeJSON(w, 200, out)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, m := range data.Mentions {
		markup, err := s.Presentation.Markup("prompt-item", struct {
			Mention richtext.Mention
			HTML    template.HTML
		}{m, template.HTML(richtext.MentionHTML(m))})
		if err != nil {
			s.fail(w, err)
			return
		}
		fmt.Fprint(w, markup)
	}
}
func wantsJSON(r *http.Request) bool {
	format, _ := httpcompat.Negotiate(formatInput(r), "html", "json")
	return format == "json"
}
