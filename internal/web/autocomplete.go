package web

import (
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/httpcompat"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/richtext"
)

func (s *Server) autocomplete(w http.ResponseWriter, r *http.Request, u database.User) {
	room := int64(0)
	if raw := r.Form.Get("room_id"); strings.TrimSpace(raw) != "" {
		var err error
		room, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if _, err = s.DB.Room(r.Context(), u.ID, room); err != nil {
			s.fail(w, err)
			return
		}
	}
	query := r.Form.Get("filter")
	if strings.TrimSpace(query) == "" {
		query = r.Form.Get("query")
	}
	if strings.TrimSpace(query) == "" {
		query = ""
	}
	users, err := s.DB.AutocompleteUsers(r.Context(), room, query)
	if err != nil {
		s.fail(w, err)
		return
	}
	number, _ := strconv.ParseInt(r.Form.Get("page"), 10, 64)
	number = max(1, min(number, 1000000000))
	offset := min(int64(len(users)), (number-1)*20)
	end := min(int64(len(users)), offset+20)
	format := respondFormat(w, r, "html", "json")
	if format == "" {
		return
	}
	if format == "json" {
		w.Header().Set("X-Total-Count", strconv.Itoa(len(users)))
		if number != max(1, int64((len(users)+19)/20)) {
			next := *r.URL
			q := next.Query()
			q.Set("page", strconv.FormatInt(number+1, 10))
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
		for _, user := range users[offset:end] {
			m := s.mention(user)
			out = append(out, suggestion{template.HTMLEscapeString(user.Name), user.ID, s.origin(r) + m.Avatar, m.SGID})
		}
		writeJSON(w, 200, out)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, user := range users[offset:end] {
		m := s.mention(user)
		markup, err := s.markup("prompt-item", struct {
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
