package web

import (
	"context"
	"errors"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

type structuredParamsKey struct{}

func needsStructuredParams(r *http.Request) bool {
	method := r.Method
	if method == "POST" {
		values := r.PostForm["_method"]
		if len(values) > 0 {
			switch override := strings.ToUpper(values[len(values)-1]); override {
			case "PATCH", "PUT", "DELETE":
				method = override
			}
		}
	}
	selection := *r
	selection.Method = method
	route, _, _ := recognizeRequest(&selection)
	if route == nil {
		return false
	}
	switch route.Action {
	case "active_storage::direct_uploads_create", "rooms::opens::create", "rooms::opens::update",
		"rooms::closeds::create", "rooms::closeds::update", "users::push_subscriptions::create":
		return true
	}
	return false
}

// Strong nested attributes need a tree, not flattened url.Values.
// Keep other form consumers on their existing flat representation.
func parseRequestForm(r *http.Request) error {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media == "application/x-www-form-urlencoded" {
		// Read the bounded body once so route selection can honor _method before
		// choosing a structured representation, just like the final dispatcher.
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		// ParseForm must not reread an unwrapped body and impose its independent
		// 10 MiB cap. The server's MaxBytesReader already bounded this read.
		r.PostForm, err = url.ParseQuery(string(raw))
		if err != nil {
			return err
		}
		if err = recordFormNulls(r, string(raw)); err != nil {
			return err
		}
		if !needsStructuredParams(r) {
			return r.ParseForm()
		}
		params, err := formTree(string(raw))
		if err != nil {
			return err
		}
		if err := storeStructuredParams(r, params); err != nil {
			return err
		}
	}
	return r.ParseForm()
}

// Merge sources before strong attributes are selected. JSON wrapping uses only
// body parameters and must not replace an explicitly supplied body or query root.
func storeStructuredParams(r *http.Request, params map[string]any) error {
	query, err := formTree(r.URL.RawQuery)
	if err != nil {
		return err
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	route, _, _ := recognizeRequest(r)
	if media == "application/json" && route != nil && route.Action == "users::push_subscriptions::create" {
		_, bodyRoot := params["push_subscription"]
		_, queryRoot := query["push_subscription"]
		if !bodyRoot && !queryRoot {
			wrapped := maps.Clone(params)
			for _, key := range []string{"authenticity_token", "_method", "utf8"} {
				delete(wrapped, key)
			}
			params["push_subscription"] = wrapped
		}
	}
	// Ctx::new merges query hashes at the top level, not at flattened leaves.
	for key, value := range query {
		params[key] = value
	}
	*r = *r.WithContext(context.WithValue(r.Context(), structuredParamsKey{}, params))
	return nil
}

// The valid bracket forms from kit's store_nested_param. Bare values are nil;
// repeated scalars overwrite, [] appends, and numbered keys remain hash keys.
func formTree(raw string) (map[string]any, error) {
	params := map[string]any{}
	for i, pair := range strings.Split(raw, "&") {
		if i > 0 {
			pair = strings.TrimLeft(pair, " ")
		}
		if pair == "" {
			continue
		}
		key, text, hasValue := strings.Cut(pair, "=")
		key, err := url.QueryUnescape(key)
		if err != nil {
			return nil, err
		}
		if key == "" {
			continue
		}
		var value any
		if hasValue {
			text, err = url.QueryUnescape(text)
			if err != nil {
				return nil, err
			}
			value = text
		}
		if err := storeFormValue(params, key, value); err != nil {
			return nil, err
		}
	}
	return params, nil
}

// Multipart supplies decoded field names and values in wire order. Share key
// validation and nesting with URL-encoded input without re-sorting the pairs.
func storeFormValue(params map[string]any, key string, value any) error {
	if key == "" {
		return nil
	}
	text, _ := value.(string)
	if !utf8.ValidString(key) || !utf8.ValidString(text) {
		return errors.New("invalid parameter encoding")
	}
	path := []string{key}
	if start := strings.IndexByte(key, '['); start >= 0 {
		path[0] = key[:start]
		for rest := key[start:]; rest != ""; {
			end := strings.IndexByte(rest, ']')
			if rest[0] != '[' || end < 0 || strings.ContainsAny(rest[1:end], "[]") {
				return errors.New("invalid parameter key")
			}
			path = append(path, rest[1:end])
			// A builder step consumes at most a hash key and its [] suffix.
			if len(path) > 200 {
				return errors.New("parameters too deep")
			}
			rest = rest[end+1:]
		}
	}
	if path[0] == "" {
		return errors.New("invalid parameter key")
	}
	_, err := storeFormParam(params, path, value, 0)
	return err
}

func storeFormParam(node any, path []string, value any, depth int) (any, error) {
	if len(path) == 0 {
		return value, nil
	}
	if depth >= 100 {
		return nil, errors.New("parameters too deep")
	}
	key := path[0]
	if key == "" {
		if node == nil {
			node = []any{}
		}
		items, ok := node.([]any)
		if !ok {
			return nil, errors.New("expected parameter array")
		}
		if len(path) == 1 {
			if value != nil {
				items = append(items, value)
			}
			return items, nil
		}
		// Reuse the last hash only when this child path is not already present.
		// A child containing [] always reuses it (e.g. items[][tags][]).
		var child any
		reuse := false
		if len(items) > 0 {
			if last, ok := items[len(items)-1].(map[string]any); ok {
				reuse = !formHasPath(last, path[1:])
				if reuse {
					child = last
				}
			}
		}
		child, err := storeFormParam(child, path[1:], value, depth+1)
		if err != nil {
			return nil, err
		}
		if reuse {
			items[len(items)-1] = child
		} else {
			items = append(items, child)
		}
		return items, nil
	}
	if node == nil {
		node = map[string]any{}
	}
	object, ok := node.(map[string]any)
	if !ok {
		return nil, errors.New("expected parameter hash")
	}
	childDepth := depth + 1
	if len(path) > 1 && path[1] == "" {
		childDepth = depth // A key's [] suffix is handled in the same builder step.
	}
	child, err := storeFormParam(object[key], path[1:], value, childDepth)
	if err != nil {
		return nil, err
	}
	object[key] = child
	return object, nil
}

func formHasPath(object map[string]any, path []string) bool {
	for _, key := range path {
		if key == "" {
			return false
		}
	}
	for i, key := range path {
		value, exists := object[key]
		if !exists {
			return false
		}
		if i < len(path)-1 {
			object, _ = value.(map[string]any)
		}
	}
	return true
}

// Action Dispatch's deep_munge removes nil array elements, but retains nil hash
// values and JSON scalar types. Depth has already been validated by flattening.
func formJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			value[key] = formJSONValue(child)
		}
		return value
	case []any:
		items := value[:0]
		for _, child := range value {
			if child != nil {
				items = append(items, formJSONValue(child))
			}
		}
		return items
	default:
		return value
	}
}
