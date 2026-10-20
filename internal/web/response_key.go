package web

import (
	"encoding/binary"
	"net/http"
	"os"
	"slices"
)

// The response identity is internal, not a wire format. Length framing preserves
// arbitrary header bytes and field/list boundaries without JSON reflection,
// escaping or temporary interface trees. Parsed form keys have canonical order;
// repeated values retain their order. Nil collections remain distinct from empty.
func responseKey(r *http.Request, info *requestInfo, user int64, gzip bool) string {
	key := make([]byte, 0, 512)
	for _, value := range []string{info.host, info.origin, info.target, r.URL.RequestURI()} {
		key = appendKeyString(key, value)
	}
	if r.Form == nil {
		key = binary.AppendUvarint(key, 0)
	} else {
		key = binary.AppendUvarint(key, uint64(len(r.Form))+1)
		names := make([]string, 0, len(r.Form))
		for name := range r.Form {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			key = appendKeyString(key, name)
			key = appendKeyStrings(key, r.Form[name])
		}
	}
	key = binary.LittleEndian.AppendUint64(key, uint64(user))
	for _, name := range []string{"Cookie", "Accept", "Content-Type", "Turbo-Frame"} {
		key = appendKeyStrings(key, r.Header.Values(name))
	}
	for _, value := range []string{r.UserAgent(), r.Header.Get("Origin"), r.Header.Get("X-Requested-With"), os.Getenv("GIT_REVISION")} {
		key = appendKeyString(key, value)
	}
	if gzip {
		key = append(key, 1)
	} else {
		key = append(key, 0)
	}
	if len(key) > 8192 {
		return ""
	}
	return string(key)
}

func appendKeyString(key []byte, value string) []byte {
	key = binary.AppendUvarint(key, uint64(len(value)))
	return append(key, value...)
}

func appendKeyStrings(key []byte, values []string) []byte {
	if values == nil {
		return binary.AppendUvarint(key, 0)
	}
	key = binary.AppendUvarint(key, uint64(len(values))+1)
	for _, value := range values {
		key = appendKeyString(key, value)
	}
	return key
}
