package cable

import (
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// htmlSafe spells out escape_html_entities_in_json's escapes of <, > and &, so the expected
// JSON below can be written readably.
var htmlSafe = strings.NewReplacer("<", `\`+`u003c`, ">", `\`+`u003e`, "&", `\`+`u0026`)

const (
	lineSeparator      = "\xe2\x80\xa8" // U+2028
	paragraphSeparator = "\xe2\x80\xa9" // U+2029
	replacement        = "\xef\xbf\xbd" // U+FFFD
)

// The reference's frames (reference/crates/cable/src/protocol.rs and its tests), byte for byte.
func TestProtocolMessages(t *testing.T) {
	identifier := `{"channel":"RoomChannel","room_id":1}`
	for _, c := range []struct{ got, want string }{
		{string(ping(1700000000)), `{"type":"ping","message":1700000000}`},
		{string(confirmation(identifier)), `{"identifier":"{\"channel\":\"RoomChannel\",\"room_id\":1}","type":"confirm_subscription"}`},
		{string(rejection(identifier)), `{"identifier":"{\"channel\":\"RoomChannel\",\"room_id\":1}","type":"reject_subscription"}`},
		{string(message(appendString(nil, identifier), []byte(`{"id":1}`))), `{"identifier":"{\"channel\":\"RoomChannel\",\"room_id\":1}","message":{"id":1}}`},
		{string(readRoom(7)), `{"room_id":7}`},
		{string(typing("start", database.User{ID: 3, Name: `Kevin "K" <k&k>`})), htmlSafe.Replace(`{"action":"start","user":{"id":3,"name":"Kevin \"K\" <k&k>"}}`)},
	} {
		if c.got != c.want {
			t.Errorf("got  %s\nwant %s", c.got, c.want)
		}
	}
}

// ActiveSupport::JSON.encode as the reference does it: JSON.generate's escapes plus <, > and &,
// with U+2028/U+2029 left alone.
func TestEncode(t *testing.T) {
	for _, c := range []struct {
		value any
		want  string
	}{
		{`<turbo-stream action="append" target="a&b"><template>it's ☃</template></turbo-stream>`,
			htmlSafe.Replace(`"<turbo-stream action=\"append\" target=\"a&b\"><template>it's ☃</template></turbo-stream>"`)},
		{"back\\slash \b\f\n\r\t \x00\x1f\x7f", `"back\\slash \b\f\n\r\t \u0000\u001f` + "\x7f" + `"`},
		{"line" + lineSeparator + "paragraph" + paragraphSeparator, `"line` + lineSeparator + "paragraph" + paragraphSeparator + `"`},
		{"bad \xff byte", `"bad ` + replacement + ` byte"`},
		{map[string]any{"roomId": 5, "html": "<b>"}, htmlSafe.Replace(`{"html":"<b>","roomId":5}`)},
	} {
		got, err := encode(c.value)
		if err != nil || string(got) != c.want {
			t.Errorf("encode(%q) = %s, %v; want %s", c.value, got, err, c.want)
		}
	}
}
