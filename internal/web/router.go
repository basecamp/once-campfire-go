package web

import (
	"net/http"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Bind protocol policy once to the ordered contract table. Dispatch never
// recognizes a second path or decodes a second set of controller parameters.
func (s *Server) bindRoutes() []http.HandlerFunc {
	notFound := func(w http.ResponseWriter, r *http.Request) { publicError(w, r, 404) }
	missing := func(w http.ResponseWriter, r *http.Request) { publicError(w, r, 500) }
	authMissing := s.auth(func(w http.ResponseWriter, r *http.Request, _ database.User) {
		missing(w, r)
	})
	cable := s.auth(s.serveCable)
	actions := map[string]http.HandlerFunc{
		"action_not_found":                                      notFound,
		"missing_controller":                                    missing,
		"welcome::show":                                         s.auth(s.home),
		"first_runs::show":                                      s.browserCheck(s.setupForm),
		"first_runs::create":                                    s.browserCheck(s.setup),
		"sessions::new":                                         s.browserCheck(s.loginForm),
		"sessions::create":                                      s.browserCheck(s.login),
		"sessions::destroy":                                     s.auth(s.logout),
		"sessions::transfers::show":                             s.browserCheck(s.transfer),
		"sessions::transfers::update":                           s.browserCheck(s.transfer),
		"accounts::edit":                                        s.auth(s.accountForm),
		"accounts::update":                                      s.auth(s.updateAccount),
		"accounts::users::index":                                s.auth(s.accountUsers),
		"accounts::users::update":                               s.auth(s.manageUser),
		"accounts::users::destroy":                              s.auth(s.manageUser),
		"accounts::bots::index":                                 s.auth(s.bots),
		"accounts::bots::new":                                   s.auth(s.botForm),
		"accounts::bots::edit":                                  s.auth(s.botForm),
		"accounts::bots::create":                                s.auth(s.saveBot),
		"accounts::bots::update":                                s.auth(s.saveBot),
		"accounts::bots::destroy":                               s.auth(s.saveBot),
		"accounts::bots::keys::update":                          s.auth(s.rotateBot),
		"accounts::join_codes::create":                          s.auth(s.resetJoinCode),
		"accounts::logos::show":                                 s.browserCheck(s.logo),
		"accounts::logos::destroy":                              s.auth(s.deleteLogo),
		"accounts::custom_styles::edit":                         s.auth(s.customStyles),
		"accounts::custom_styles::update":                       s.auth(s.customStyles),
		"users::new":                                            s.browserCheck(s.join),
		"users::create":                                         s.browserCheck(s.join),
		"users::show":                                           s.auth(s.showUser),
		"users::avatars::show":                                  s.auth(s.avatar),
		"users::avatars::destroy":                               s.auth(s.deleteAvatar),
		"users::bans::create":                                   s.auth(s.banUser),
		"users::bans::destroy":                                  s.auth(s.banUser),
		"users::sidebars::show":                                 s.auth(s.sidebar),
		"users::profiles::show":                                 s.auth(s.profile),
		"users::profiles::update":                               s.auth(s.profile),
		"users::push_subscriptions::index":                      s.auth(s.pushSubscriptions),
		"users::push_subscriptions::create":                     s.auth(s.pushSubscriptions),
		"users::push_subscriptions::destroy":                    s.auth(s.deletePushSubscription),
		"users::push_subscriptions::test_notifications::create": s.auth(s.testPushNotification),
		"autocompletable::users::index":                         s.auth(s.autocomplete),
		"qr_code::show":                                         s.browserCheck(s.qrCode),
		"rooms::index":                                          s.auth(s.roomsIndex),
		"rooms::show":                                           s.auth(s.room),
		"rooms::destroy":                                        s.auth(s.deleteRoom),
		"rooms::destroy_without_room":                           authMissing,
		"rooms::opens::new":                                     s.auth(s.roomForm),
		"rooms::opens::edit":                                    s.auth(s.roomForm),
		"rooms::opens::create":                                  s.auth(s.saveRoom),
		"rooms::opens::update":                                  s.auth(s.saveRoom),
		"rooms::opens::show":                                    s.auth(s.redirectRoom),
		"rooms::closeds::new":                                   s.auth(s.roomForm),
		"rooms::closeds::edit":                                  s.auth(s.roomForm),
		"rooms::closeds::create":                                s.auth(s.saveRoom),
		"rooms::closeds::update":                                s.auth(s.saveRoom),
		"rooms::closeds::show":                                  s.auth(s.redirectRoom),
		"rooms::directs::new":                                   s.auth(s.roomForm),
		"rooms::directs::edit":                                  s.auth(s.roomForm),
		"rooms::directs::create":                                s.auth(s.saveRoom),
		"rooms::directs::show":                                  authMissing,
		"rooms::directs::destroy":                               s.auth(s.deleteRoom),
		"rooms::involvements::show":                             s.auth(s.involvement),
		"rooms::involvements::update":                           s.auth(s.involvement),
		"rooms::refreshes::show":                                s.auth(s.refreshRoom),
		"messages::index":                                       s.auth(s.messages),
		"messages::create":                                      s.auth(s.createMessage),
		"messages::show":                                        s.auth(s.showMessage),
		"messages::edit":                                        s.auth(s.editMessage),
		"messages::update":                                      s.auth(s.updateMessage),
		"messages::destroy":                                     s.auth(s.deleteMessage),
		"messages::boosts::index":                               s.auth(s.boosts),
		"messages::boosts::new":                                 s.auth(s.newBoost),
		"messages::boosts::create":                              s.auth(s.createBoost),
		"messages::boosts::destroy":                             s.auth(s.deleteBoost),
		"messages::by_bots::index":                              s.botRequest,
		"messages::by_bots::create":                             s.botRequest,
		"messages::by_bots::update":                             s.botRequest,
		"messages::by_bots::destroy":                            s.botRequest,
		"messages::boosts::by_bots::create":                     s.botRequest,
		"messages::boosts::by_bots::destroy":                    s.botRequest,
		"searches::index":                                       s.auth(s.search),
		"searches::create":                                      s.auth(s.search),
		"searches::clear":                                       s.auth(s.search),
		"unfurl_links::create":                                  s.auth(s.unfurl),
		"pwa::manifest":                                         s.browserCheck(s.manifest),
		"pwa::service_worker":                                   s.browserCheck(s.serviceWorker),
		"health::show":                                          s.health,
		"active_storage::blobs_redirect":                        s.blobDownload,
		"active_storage::blobs_proxy":                           s.blobDownload,
		"active_storage::representations_redirect":              s.representation,
		"active_storage::representations_proxy":                 s.representation,
		"active_storage::disk_show":                             s.diskDownload,
		"active_storage::disk_update":                           s.storageAuth(s.diskUpload),
		"active_storage::direct_uploads_create":                 s.storageAuth(s.directUpload),
		"mailbox::conductor":                                    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) },
		"mailbox::ingress_not_configured":                       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) },
		"cable::show": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.EscapedPath() != "/cable" || (r.Method != "GET" && r.Method != "HEAD") {
				http.NotFound(w, r)
				return
			}
			cable(w, r)
		},
	}
	for action, text := range map[string]string{
		"turbo_native::recede": "Going back…", "turbo_native::resume": "Staying put…", "turbo_native::refresh": "Refreshing…",
	} {
		actions[action] = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(text))
		}
	}
	dispatch := make([]http.HandlerFunc, len(contracts)+1)
	for i := range contracts {
		dispatch[i] = actions[contracts[i].Action]
		if dispatch[i] == nil {
			panic("unbound route action: " + contracts[i].Action)
		}
	}
	dispatch[cableRoute.index] = actions[cableRoute.Action]
	return dispatch
}
