package cable

import (
	"encoding/json"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/coder/websocket"
)

// The reference's limits and timings (reference/crates/cable/src/server.rs Config::default,
// connection.rs, socket.rs).
const (
	// beatInterval is ActionCable::Server::Connections::BEAT_INTERVAL.
	beatInterval = 3 * time.Second
	// streamCapacity is how far one subscription may fall behind its stream before its
	// connection is closed with reconnect: true.
	streamCapacity = 256
	// maxWriteBatch bounds the frames one socket write carries.
	maxWriteBatch = 64
	// writeTimeout is how long a write may wait for a client to read.
	writeTimeout = 30 * time.Second
	// closeTimeout is how long Close waits for connections to finish closing.
	closeTimeout     = 5 * time.Second
	maxSubscriptions = 64
	maxIdentifier    = 4096
)

// The Action Cable messages, byte for byte as the reference writes them
// (reference/crates/cable/src/protocol.rs): Ruby hashes in literal key order, encoded as
// ActiveSupport::JSON.encode does.
var (
	welcome         = frame([]byte(`{"type":"welcome"}`))
	remoteReconnect = frame([]byte(`{"type":"disconnect","reason":"remote","reconnect":true}`))
	remoteFinal     = frame([]byte(`{"type":"disconnect","reason":"remote","reconnect":false}`))
	serverRestart   = frame([]byte(`{"type":"disconnect","reason":"server_restart","reconnect":true}`))
	unauthorized    = frame([]byte(`{"type":"disconnect","reason":"unauthorized","reconnect":false}`))
	// lagged closes a connection whose subscription fell behind (Connection::Base#close
	// without a reason).
	lagged = frame([]byte(`{"type":"disconnect","reason":null,"reconnect":true}`))
)

func frame(text []byte) *websocket.PreparedMessage {
	return websocket.NewPreparedMessage(websocket.MessageText, text)
}

// ping is Connection::Base#beat's message.
func ping(unix int64) []byte {
	return append(strconv.AppendInt([]byte(`{"type":"ping","message":`), unix, 10), '}')
}

func confirmation(identifier string) []byte {
	return append(appendString([]byte(`{"identifier":`), identifier), `,"type":"confirm_subscription"}`...)
}

func rejection(identifier string) []byte {
	return append(appendString([]byte(`{"identifier":`), identifier), `,"type":"reject_subscription"}`...)
}

// message wraps an encoded broadcast for the subscribers with this encoded identifier.
func message(encodedIdentifier, payload []byte) []byte {
	b := make([]byte, 0, len(`{"identifier":,"message":}`)+len(encodedIdentifier)+len(payload))
	b = append(append(b, `{"identifier":`...), encodedIdentifier...)
	return append(append(append(b, `,"message":`...), payload...), '}')
}

// readRoom is PresenceChannel's broadcast to the user's other windows: {room_id:}.
func readRoom(room int64) []byte {
	return append(strconv.AppendInt([]byte(`{"room_id":`), room, 10), '}')
}

// typing is TypingNotificationsChannel's broadcast: {action:, user: {id:, name:}}.
func typing(action string, user database.User) []byte {
	b := append(append([]byte(`{"action":`), appendString(nil, action)...), `,"user":{"id":`...)
	b = append(strconv.AppendInt(b, user.ID, 10), `,"name":`...)
	return append(appendString(b, user.Name), "}}"...)
}

// encode is ActiveSupport::JSON.encode for a broadcast's payload. Strings (Turbo Stream markup)
// go through appendString, anything else through encoding/json, which escapes <, > and & the
// same way.
func encode(value any) ([]byte, error) {
	if s, ok := value.(string); ok {
		return appendString(nil, s), nil
	}
	return json.Marshal(value)
}

// appendString appends s as a JSON string the way the reference encodes one
// (rails_compat::json::encode): JSON.generate's escapes, then <, > and & as six-character
// unicode escapes (escape_html_entities_in_json). U+2028 and U+2029 are left alone. Invalid
// UTF-8 becomes U+FFFD.
func appendString(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			if b >= 0x20 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '"', '\\':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = utf8.AppendRune(append(dst, s[start:i]...), utf8.RuneError)
			i++
			start = i
			continue
		}
		i += size
	}
	return append(append(dst, s[start:]...), '"')
}
