package views

import (
	"bytes"
	"encoding/json"
)

// The Jbuilder views: messages/_message.json, messages/by_bots/{index,show}.json,
// messages/boosts/_boost.json and messages/boosts/by_bots/show.json (the reference's
// messages/json.rs). Field order is the JSON key order Jbuilder emits.

// UserJSON is users/_user.json.jbuilder: json.(user, :id, :name, :role) and avatar_url.
type UserJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// "member", "administrator" or "bot".
	Role string `json:"role"`
	// fresh_user_avatar_url(user): a full URL.
	AvatarURL string `json:"avatar_url"`
}

// MessageJSON is messages/_message.json.jbuilder.
type MessageJSON struct {
	ID int64 `json:"id"`
	// message.created_at.utc, formatted by JSONTime.
	CreatedAt string          `json:"created_at"`
	Body      MessageBodyJSON `json:"body"`
	Creator   UserJSON        `json:"creator"`
	Room      IDJSON          `json:"room"`
	// room_message_url(message.room, message).
	URL string `json:"url"`
}

type MessageBodyJSON struct {
	// message.plain_text_body.
	PlainText string `json:"plain_text"`
	// message.body.to_s: the rich text rendered with its layout.
	HTML string `json:"html"`
}

type IDJSON struct {
	ID int64 `json:"id"`
}

// BoostJSON is messages/boosts/_boost.json.jbuilder.
type BoostJSON struct {
	ID      int64  `json:"id"`
	Content string `json:"content"`
	// boost.created_at.utc, formatted by JSONTime.
	CreatedAt string           `json:"created_at"`
	Booster   UserJSON         `json:"booster"`
	Message   BoostMessageJSON `json:"message"`
}

type BoostMessageJSON struct {
	ID int64 `json:"id"`
	// room_message_url(boost.message.room, boost.message).
	URL string `json:"url"`
}

// MessagesByBotsIndexJSON is messages/by_bots/index.json.jbuilder:
// json.array! @messages, partial: "messages/message".
func MessagesByBotsIndexJSON(messages []MessageJSON) string {
	if messages == nil {
		messages = []MessageJSON{}
	}
	return railsJSON(messages)
}

// MessagesByBotsShowJSON is messages/by_bots/show.json.jbuilder.
func MessagesByBotsShowJSON(message *MessageJSON) string { return railsJSON(message) }

// MessagesBoostsByBotsShowJSON is messages/boosts/by_bots/show.json.jbuilder.
func MessagesBoostsByBotsShowJSON(boost *BoostJSON) string { return railsJSON(boost) }

// A Jbuilder fragment's payload is its JSON: these are the sizes FragmentCache.FetchValue takes.
func (u *UserJSON) CacheSize() int    { return serializedSize(u) }
func (m *MessageJSON) CacheSize() int { return serializedSize(m) }
func (b *BoostJSON) CacheSize() int   { return serializedSize(b) }

// railsJSON is ActiveSupport::JSON.encode (the reference's rails_compat::json::encode): the json
// gem's JSON.generate, plus <, > and & escaped as \u003c, \u003e and \u0026. U+2028/U+2029 are
// not escaped: load_defaults 8.1+ turns escape_js_separators_in_json off.
func railsJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(unescapeJSSeparators(data))
}

// serializedSize is the length of value's JSON: the payload size of a Jbuilder value.
func serializedSize(value any) int {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 0
	}
	return b.Len() - 1 // Encode's newline
}

// unescapeJSSeparators writes the \u2028 and \u2029 escapes encoding/json always makes as the
// characters themselves, as the json gem leaves them.
func unescapeJSSeparators(data []byte) []byte {
	if !bytes.Contains(data, []byte(`\u202`)) {
		return data
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' || i+1 == len(data) {
			out = append(out, data[i])
			continue
		}
		if i+5 < len(data) && data[i+1] == 'u' && string(data[i+2:i+5]) == "202" && (data[i+5] == '8' || data[i+5] == '9') {
			if data[i+5] == '8' {
				out = append(out, "\u2028"...)
			} else {
				out = append(out, "\u2029"...)
			}
			i += 5
			continue
		}
		out = append(out, data[i], data[i+1])
		i++
	}
	return out
}
