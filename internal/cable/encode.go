package cable

import "unicode/utf8"

// hexDigits are the lowercase hex digits encoding/json uses for \u escapes;
// the escaper below must stay byte-for-byte identical to encoding/json's
// HTML-safe string escaping (the Marshal default), which is what the frames
// this package has always been encoded with (ENGINE-57 pins that with
// TestAppendJSONStringMatchesMarshal and TestPublishFrameBytesMatchLegacy).
const hexDigits = "0123456789abcdef"

// jsonSafeSet marks the ASCII bytes encoding/json writes verbatim inside a
// quoted string under HTML-safe escaping; everything below 0x20 (including
// <, >, &, ", \) is escaped. This is encoding/json's htmlSafeSet, copied
// from the Go standard library (BSD-3-Clause, https://go.dev/LICENSE):
// https://cs.opensource.google/go/go/+/master:src/encoding/json/tables.go
var jsonSafeSet = [utf8.RuneSelf]bool{
	' ': true, '!': true, '"': false, '#': true, '$': true, '%': true,
	'&': false, '\'': true, '(': true, ')': true, '*': true, '+': true,
	',': true, '-': true, '.': true, '/': true,
	'0': true, '1': true, '2': true, '3': true, '4': true,
	'5': true, '6': true, '7': true, '8': true, '9': true,
	':': true, ';': true, '<': false, '=': true, '>': false, '?': true,
	'@': true,
	'A': true, 'B': true, 'C': true, 'D': true, 'E': true, 'F': true,
	'G': true, 'H': true, 'I': true, 'J': true, 'K': true, 'L': true,
	'M': true, 'N': true, 'O': true, 'P': true, 'Q': true, 'R': true,
	'S': true, 'T': true, 'U': true, 'V': true, 'W': true, 'X': true,
	'Y': true, 'Z': true,
	'[': true, '\\': false, ']': true, '^': true, '_': true, '`': true,
	'a': true, 'b': true, 'c': true, 'd': true, 'e': true, 'f': true,
	'g': true, 'h': true, 'i': true, 'j': true, 'k': true, 'l': true,
	'm': true, 'n': true, 'o': true, 'p': true, 'q': true, 'r': true,
	's': true, 't': true, 'u': true, 'v': true, 'w': true, 'x': true,
	'y': true, 'z': true,
	'{': true, '|': true, '}': true, '~': true, '\u007f': true,
}

// appendJSONString appends s to dst as a JSON string literal, escaping
// exactly like encoding/json does with HTML-safe escaping on: short escapes
// for \b \f \n \r \t, \u00XX for other controls and for the HTML characters
// < > &, \u2028/\u2029 for the JS-dangerous separators, raw U+FFFD for
// invalid UTF-8 (the Go 1.27 toolchain behavior), and verbatim UTF-8 for
// everything else. It mirrors encoding/json.encodeState.appendString
// (escapeHTML=true) on this repo's toolchain; the dedicated test pins
// byte-identity against json.Marshal.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if jsonSafeSet[b] {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '\\', '"':
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
				// Controls other than the short escapes above, and
				// (under HTML-safe escaping) <, > and &: \u00XX.
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xF])
			}
			i++
			start = i
			continue
		}
		n := len(s) - i
		if n > utf8.UTFMax {
			n = utf8.UTFMax
		}
		r, size := utf8.DecodeRuneInString(s[i : i+n])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			// encoding/json on the Go 1.27 toolchain this repo builds with
			// writes the raw UTF-8 encoding of the replacement character
			// (EF BF BD, unescaped) for invalid bytes; the pre-ENGINE-57
			// frames therefore contained those raw bytes, and the escaper
			// must reproduce them (go1.26 and earlier escaped \ufffd; the
			// pin test fails loudly if built with such a toolchain).
			dst = append(dst, "\uFFFD"...)
			i += size
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	dst = append(dst, '"')
	return dst
}

// appendFrameUTF8 appends the broadcast wire frame for one identifier whose
// payload is already a string: {"identifier":<escaped id>,"message":<escaped
// payload>}. Both escapes go straight into dst, so a broadcast encodes its
// payload exactly once and builds every identifier's wrapper in the same
// reusable buffer.
func appendFrameUTF8(dst []byte, identifier, payload string) []byte {
	dst = append(dst, `{"identifier":`...)
	dst = appendJSONString(dst, identifier)
	dst = append(dst, `,"message":`...)
	dst = appendJSONString(dst, payload)
	dst = append(dst, '}')
	return dst
}

// appendFrameJSON is appendFrameUTF8 for payloads already encoded as JSON
// (json.Marshal output for the non-string message case): the payload bytes
// are spliced without re-escaping.
func appendFrameJSON(dst []byte, identifier string, messageJSON []byte) []byte {
	dst = append(dst, `{"identifier":`...)
	dst = appendJSONString(dst, identifier)
	dst = append(dst, `,"message":`...)
	dst = append(dst, messageJSON...)
	dst = append(dst, '}')
	return dst
}
