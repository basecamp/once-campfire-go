package web

import (
	"net/http"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func (s *Server) registerAccountRoutes() {
	s.mux.HandleFunc("GET /account/edit", s.auth(s.accountsEdit))
	s.mux.HandleFunc("PATCH /account", s.auth(s.accountsUpdate))
	s.mux.HandleFunc("PUT /account", s.auth(s.accountsUpdate))
	s.mux.HandleFunc("POST /account/join_code", s.auth(s.accountsJoinCodesCreate))
	s.mux.HandleFunc("GET /account/custom_styles/edit", s.auth(s.accountsCustomStylesEdit))
	s.mux.HandleFunc("PATCH /account/custom_styles", s.auth(s.accountsCustomStylesUpdate))
	s.mux.HandleFunc("PUT /account/custom_styles", s.auth(s.accountsCustomStylesUpdate))
	s.mux.HandleFunc("GET /users/{user_id}/profile", s.auth(s.usersProfileShow))
	s.mux.HandleFunc("PATCH /users/{user_id}/profile", s.auth(s.usersProfileUpdate))
	s.mux.HandleFunc("PUT /users/{user_id}/profile", s.auth(s.usersProfileUpdate))
	s.mux.HandleFunc("GET /users/{id}", s.auth(s.usersShow))
	s.mux.HandleFunc("POST /users/{user_id}/ban", s.auth(s.usersBan(true)))
	s.mux.HandleFunc("DELETE /users/{user_id}/ban", s.auth(s.usersBan(false)))
	s.mux.HandleFunc("PATCH /account/users/{user}", s.auth(s.accountsUsersUpdate))
	s.mux.HandleFunc("PUT /account/users/{user}", s.auth(s.accountsUsersUpdate))
	s.mux.HandleFunc("DELETE /account/users/{user}", s.auth(s.accountsUsersDestroy))
	s.mux.HandleFunc("GET /join/{join_code}", s.browserCheck(s.usersNew))
	s.mux.HandleFunc("POST /join/{join_code}", s.browserCheck(s.usersCreate))
	s.mux.HandleFunc("GET /account/users", s.auth(s.accountsUsersIndex))
	s.mux.HandleFunc("GET /account/bots", s.auth(s.accountsBotsIndex))
	s.mux.HandleFunc("GET /account/bots/new", s.auth(s.accountsBotsNew))
	s.mux.HandleFunc("GET /account/bots/{bot}/edit", s.auth(s.accountsBotsEdit))
	s.mux.HandleFunc("POST /account/bots", s.auth(s.accountsBotsCreate))
	s.mux.HandleFunc("PATCH /account/bots/{bot}", s.auth(s.accountsBotsUpdate))
	s.mux.HandleFunc("PUT /account/bots/{bot}", s.auth(s.accountsBotsUpdate))
	s.mux.HandleFunc("DELETE /account/bots/{bot}", s.auth(s.accountsBotsDestroy))
	s.mux.HandleFunc("PATCH /account/bots/{bot}/key", s.auth(s.accountsBotsKeysUpdate))
	s.mux.HandleFunc("PUT /account/bots/{bot}/key", s.auth(s.accountsBotsKeysUpdate))
	s.mux.HandleFunc("GET /session/transfers/{token}", s.browserCheck(s.sessionTransfersShow))
	s.mux.HandleFunc("PATCH /session/transfers/{token}", s.browserCheck(s.sessionTransfersUpdate))
	s.mux.HandleFunc("PUT /session/transfers/{token}", s.browserCheck(s.sessionTransfersUpdate))
}
func administrator(w http.ResponseWriter, u database.User) bool {
	if u.Role != 1 {
		http.Error(w, "Forbidden", 403)
		return false
	}
	return true
}
