package views

import (
	"strconv"
	"strings"
)

// Path helpers mirroring reference/config/routes.rb (the reference's campfire_routes crate), shared
// by controllers (redirects) and views (links): the Rails *_path helpers, named Route + the
// helper's name with the _path suffix dropped.

// routeID is a path segment for a record id.
func routeID(id int64) string { return strconv.FormatInt(id, 10) }

func RouteRoot() string     { return "/" }
func RouteFirstRun() string { return "/first_run" }

func RouteSession() string                  { return "/session" }
func RouteNewSession() string               { return "/session/new" }
func RouteSessionTransfer(id string) string { return "/session/transfers/" + id }

func RouteAccount() string                  { return "/account" }
func RouteEditAccount() string              { return "/account/edit" }
func RouteAccountUsers() string             { return "/account/users" }
func RouteAccountUser(id int64) string      { return "/account/users/" + routeID(id) }
func RouteEditAccountUser(id int64) string  { return "/account/users/" + routeID(id) + "/edit" }
func RouteAccountBots() string              { return "/account/bots" }
func RouteNewAccountBot() string            { return "/account/bots/new" }
func RouteAccountBot(id int64) string       { return "/account/bots/" + routeID(id) }
func RouteEditAccountBot(id int64) string   { return "/account/bots/" + routeID(id) + "/edit" }
func RouteAccountBotKey(botID int64) string { return "/account/bots/" + routeID(botID) + "/key" }
func RouteAccountJoinCode() string          { return "/account/join_code" }
func RouteAccountLogo() string              { return "/account/logo" }
func RouteAccountCustomStyles() string      { return "/account/custom_styles" }
func RouteEditAccountCustomStyles() string  { return "/account/custom_styles/edit" }

func RouteJoin(joinCode string) string { return "/join/" + joinCode }
func RouteQrCode(id string) string     { return "/qr_code/" + id }

func RouteUser(id int64) string                 { return "/users/" + routeID(id) }
func RouteUserAvatar(userID int64) string       { return "/users/" + routeID(userID) + "/avatar" }
func RouteUserBan(userID int64) string          { return "/users/" + routeID(userID) + "/ban" }
func RouteUserSidebar() string                  { return "/users/me/sidebar" }
func RouteUserProfile() string                  { return "/users/me/profile" }
func RouteEditUserProfile() string              { return "/users/me/profile/edit" }
func RouteUserPushSubscriptions() string        { return "/users/me/push_subscriptions" }
func RouteUserPushSubscription(id int64) string { return "/users/me/push_subscriptions/" + routeID(id) }
func RouteUserPushSubscriptionTestNotifications(pushSubscriptionID int64) string {
	return "/users/me/push_subscriptions/" + routeID(pushSubscriptionID) + "/test_notifications"
}

func RouteAutocompletableUsers() string { return "/autocompletable/users" }

func RouteRooms() string                      { return "/rooms" }
func RouteNewRoom() string                    { return "/rooms/new" }
func RouteRoom(id int64) string               { return "/rooms/" + routeID(id) }
func RouteEditRoom(id int64) string           { return "/rooms/" + routeID(id) + "/edit" }
func RouteRoomMessages(roomID int64) string   { return "/rooms/" + routeID(roomID) + "/messages" }
func RouteNewRoomMessage(roomID int64) string { return "/rooms/" + routeID(roomID) + "/messages/new" }
func RouteRoomMessage(roomID, id int64) string {
	return "/rooms/" + routeID(roomID) + "/messages/" + routeID(id)
}
func RouteEditRoomMessage(roomID, id int64) string {
	return "/rooms/" + routeID(roomID) + "/messages/" + routeID(id) + "/edit"
}
func RouteRoomBotMessages(roomID int64, botKey string) string {
	return "/rooms/" + routeID(roomID) + "/" + botKey + "/messages"
}
func RouteRoomBotMessage(roomID int64, botKey string, id int64) string {
	return "/rooms/" + routeID(roomID) + "/" + botKey + "/messages/" + routeID(id)
}
func RouteRoomBotMessageBoosts(roomID int64, botKey string, messageID int64) string {
	return "/rooms/" + routeID(roomID) + "/" + botKey + "/messages/" + routeID(messageID) + "/boosts"
}
func RouteRoomBotMessageBoost(roomID int64, botKey string, messageID, id int64) string {
	return "/rooms/" + routeID(roomID) + "/" + botKey + "/messages/" + routeID(messageID) + "/boosts/" + routeID(id)
}
func RouteRoomRefresh(roomID int64) string     { return "/rooms/" + routeID(roomID) + "/refresh" }
func RouteRoomSettings(roomID int64) string    { return "/rooms/" + routeID(roomID) + "/settings" }
func RouteRoomInvolvement(roomID int64) string { return "/rooms/" + routeID(roomID) + "/involvement" }
func RouteRoomAtMessage(roomID, messageID int64) string {
	return "/rooms/" + routeID(roomID) + "/@" + routeID(messageID)
}

func RouteRoomsOpens() string              { return "/rooms/opens" }
func RouteNewRoomsOpen() string            { return "/rooms/opens/new" }
func RouteRoomsOpen(id int64) string       { return "/rooms/opens/" + routeID(id) }
func RouteEditRoomsOpen(id int64) string   { return "/rooms/opens/" + routeID(id) + "/edit" }
func RouteRoomsCloseds() string            { return "/rooms/closeds" }
func RouteNewRoomsClosed() string          { return "/rooms/closeds/new" }
func RouteRoomsClosed(id int64) string     { return "/rooms/closeds/" + routeID(id) }
func RouteEditRoomsClosed(id int64) string { return "/rooms/closeds/" + routeID(id) + "/edit" }
func RouteRoomsDirects() string            { return "/rooms/directs" }
func RouteNewRoomsDirect() string          { return "/rooms/directs/new" }
func RouteRoomsDirect(id int64) string     { return "/rooms/directs/" + routeID(id) }
func RouteEditRoomsDirect(id int64) string { return "/rooms/directs/" + routeID(id) + "/edit" }

func RouteMessages() string                     { return "/messages" }
func RouteMessage(id int64) string              { return "/messages/" + routeID(id) }
func RouteEditMessage(id int64) string          { return "/messages/" + routeID(id) + "/edit" }
func RouteMessageBoosts(messageID int64) string { return "/messages/" + routeID(messageID) + "/boosts" }
func RouteNewMessageBoost(messageID int64) string {
	return "/messages/" + routeID(messageID) + "/boosts/new"
}
func RouteMessageBoost(messageID, id int64) string {
	return "/messages/" + routeID(messageID) + "/boosts/" + routeID(id)
}

func RouteSearches() string         { return "/searches" }
func RouteClearSearches() string    { return "/searches/clear" }
func RouteUnfurlLink() string       { return "/unfurl_link" }
func RouteWebmanifest() string      { return "/webmanifest" }
func RouteServiceWorker() string    { return "/service-worker" }
func RouteRailsHealthCheck() string { return "/up" }

// RouteFreshUserAvatar is `direct :fresh_user_avatar`: the cache-busting avatar URL keyed by the
// signed avatar token.
func RouteFreshUserAvatar(avatarToken, updatedAtNumber string) string {
	return "/users/" + avatarToken + "/avatar?v=" + updatedAtNumber
}

// RouteFreshAccountLogo is `direct :fresh_account_logo`: v is the account's updated_at.to_fs(:number),
// when present.
func RouteFreshAccountLogo(v, size *string) string {
	var query []string
	if size != nil {
		query = append(query, "size="+*size)
	}
	if v != nil {
		query = append(query, "v="+*v)
	}
	if len(query) == 0 {
		return RouteAccountLogo()
	}
	return RouteAccountLogo() + "?" + strings.Join(query, "&")
}
