package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestResponseKeyPreservesVariantBoundaries(t *testing.T) {
	request := httptest.NewRequest("GET", "/searches?q=hello", nil)
	info := &requestInfo{host: "cache.test", origin: "http://cache.test", target: request.RequestURI}
	key := func() string { return responseKey(request, info, 1, false) }
	request.Form = url.Values{"a": {"x", "y"}, "b": {"z"}}
	first := key()
	request.Form = url.Values{"b": {"z"}, "a": {"x", "y"}}
	if key() != first {
		t.Fatal("form map insertion order changed identity")
	}
	request.Form["a"] = []string{"y", "x"}
	if key() == first {
		t.Fatal("repeated value order was lost")
	}
	for _, pair := range [][2][]string{
		{nil, {}},
		{{"a", "bc"}, {"ab", "c"}},
		{{"a\x00b"}, {"a", "b"}},
		{{"\xff"}, {"\xfe"}},
	} {
		request.Header["Accept"] = pair[0]
		left := key()
		request.Header["Accept"] = pair[1]
		if key() == left {
			t.Fatal("different header variants shared an identity", pair)
		}
	}
	request.Header.Set("Accept", strings.Repeat("x", 8193))
	if key() != "" {
		t.Fatal("oversized key retained")
	}
}
