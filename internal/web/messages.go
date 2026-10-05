package web

import (
	"context"
	"html"
	"time"
)

func (s *Server) registerMessageRoutes() {
	s.mux.HandleFunc("GET /messages", s.auth(s.messagesIndex))
	s.mux.HandleFunc("POST /messages", s.auth(s.messagesCreate))
	s.mux.HandleFunc("GET /messages/{id}", s.auth(s.messagesShow))
	s.mux.HandleFunc("GET /messages/{id}/edit", s.auth(s.messagesEdit))
	s.mux.HandleFunc("PATCH /messages/{id}", s.auth(s.messagesUpdate))
	s.mux.HandleFunc("PUT /messages/{id}", s.auth(s.messagesUpdate))
	s.mux.HandleFunc("DELETE /messages/{id}", s.auth(s.messagesDestroy))
	s.mux.HandleFunc("GET /rooms/{room_id}/messages/{id}", s.auth(s.messagesShow))
	s.mux.HandleFunc("GET /rooms/{room_id}/messages/{id}/edit", s.auth(s.messagesEdit))
	s.mux.HandleFunc("PATCH /rooms/{room_id}/messages/{id}", s.auth(s.messagesUpdate))
	s.mux.HandleFunc("PUT /rooms/{room_id}/messages/{id}", s.auth(s.messagesUpdate))
	s.mux.HandleFunc("DELETE /rooms/{room_id}/messages/{id}", s.auth(s.messagesDestroy))
	s.mux.HandleFunc("GET /messages/{message_id}/boosts", s.auth(s.boostsIndex))
	s.mux.HandleFunc("GET /messages/{message_id}/boosts/new", s.auth(s.boostsNew))
	s.mux.HandleFunc("POST /messages/{message_id}/boosts", s.auth(s.boostsCreate))
	s.mux.HandleFunc("DELETE /messages/{message_id}/boosts/{id}", s.auth(s.boostsDestroy))
	s.mux.HandleFunc("GET /rooms/{room_id}/refresh", s.auth(s.refreshShow))
}

type requestOriginKey struct{}

func stream(action, target, markup string) string {
	if action == "remove" {
		return `<turbo-stream action="remove" target="` + html.EscapeString(target) + `"></turbo-stream>`
	}
	return `<turbo-stream action="` + action + `" target="` + html.EscapeString(target) + `"><template>` + markup + `</template></turbo-stream>`
}
func (s *Server) publish(room int64, markup string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Cable.Publish(ctx, room, markup)
}
