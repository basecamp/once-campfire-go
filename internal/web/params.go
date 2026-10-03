package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

type nullParamsKey struct{}

// Retain the raw body for direct uploads and push subscriptions, whose handlers
// also consume JSON. Query parameters take precedence, as in Action Dispatch.
func parseJSONParams(r *http.Request) error {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err = decoder.Decode(&value); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	nulls := map[string]bool{}
	values := url.Values{}
	var flatten func(string, any, int) error
	flatten = func(key string, value any, depth int) error {
		if depth > 100 {
			return errors.New("parameters too deep")
		}
		switch v := value.(type) {
		case map[string]any:
			for name, child := range v {
				nested := name
				if key != "" {
					nested = key + "[" + name + "]"
				}
				if err := flatten(nested, child, depth+1); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := flatten(key+"[]", child, depth+1); err != nil {
					return err
				}
			}
		case nil:
			values.Add(key, "")
			nulls[key] = true
		case string:
			values.Add(key, v)
		default:
			values.Add(key, fmt.Sprint(v))
		}
		return nil
	}
	if _, ok := value.(map[string]any); !ok {
		value = map[string]any{"_json": value}
	}
	if err := flatten("", value, 0); err != nil {
		return err
	}
	for key, value := range values {
		r.PostForm[key] = value
		r.Form[key] = value
	}
	for key, value := range r.URL.Query() {
		r.Form[key] = value
		delete(nulls, key)
	}
	*r = *r.WithContext(context.WithValue(r.Context(), nullParamsKey{}, nulls))
	return nil
}

func nullParam(r *http.Request, key string) bool {
	nulls, _ := r.Context().Value(nullParamsKey{}).(map[string]bool)
	return nulls[key]
}

// Rack overwrites repeated scalar keys; [] keys retain all values.
func normalizeScalarParams(r *http.Request) {
	for _, values := range []url.Values{r.Form, r.PostForm} {
		for key, items := range values {
			if len(items) > 1 && !strings.HasSuffix(key, "[]") {
				values[key] = items[len(items)-1:]
			}
		}
	}
}
