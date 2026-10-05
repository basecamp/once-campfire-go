package views

import (
	"sort"
	"strconv"
	"strings"
)

// URL building that the route helpers leave to the caller: query strings (Hash#to_query) and
// format extensions (path(format: :json)).

// CGIEscape is CGI.escape: everything but A-Za-z0-9_.-~ is percent-encoded, and a space becomes "+".
func CGIEscape(s string) string { return percentEncode(s, true) }

// urlEncode is ERB::Util.url_encode: everything but A-Za-z0-9_.-~ is percent-encoded, a space as
// %20. Addressable's encode_component(s, UNRESERVED) keeps the same set.
func urlEncode(s string) string { return percentEncode(s, false) }

func percentEncode(s string, spaceAsPlus bool) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '.', c == '-', c == '~':
			b.WriteByte(c)
		case c == ' ' && spaceAsPlus:
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}

// Param is a query parameter: a scalar, or an array (`key[]=a&key[]=b`).
type Param struct {
	Key    string
	Values []string
	Many   bool
}

// ParamOne is a scalar parameter.
func ParamOne(key, value string) Param { return Param{Key: key, Values: []string{value}} }

// ParamMany is an array parameter.
func ParamMany(key string, values []string) Param { return Param{Key: key, Values: values, Many: true} }

// WithQuery is url_for's extra params: Hash#to_query, which sorts by key.
func WithQuery(path string, params ...Param) string {
	sort.SliceStable(params, func(i, j int) bool { return params[i].Key < params[j].Key })
	var pairs []string
	for _, param := range params {
		key := param.Key
		if param.Many {
			key += "[]"
		}
		key = CGIEscape(key)
		for _, value := range param.Values {
			pairs = append(pairs, key+"="+CGIEscape(value))
		}
	}
	if len(pairs) == 0 {
		return path
	}
	return path + "?" + strings.Join(pairs, "&")
}

// RoomsDirectsWithUsers is rooms_directs_path(user_ids: [ id ]).
func RoomsDirectsWithUsers(userIDs []int64) string {
	ids := make([]string, len(userIDs))
	for i, id := range userIDs {
		ids[i] = strconv.FormatInt(id, 10)
	}
	return WithQuery(RouteRoomsDirects(), ParamMany("user_ids", ids))
}

// RoomsDirectsWithUser is rooms_directs_path(user_ids: [ user.id ]).
func RoomsDirectsWithUser(userID int64) string {
	return RoomsDirectsWithUsers([]int64{userID})
}
