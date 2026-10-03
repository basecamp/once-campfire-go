package storage

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func typedValue(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		if v, ok := object["sym"]; ok {
			var s string
			json.Unmarshal(v, &s)
			return Symbol(s)
		}
		if v, ok := object["str"]; ok {
			var s string
			json.Unmarshal(v, &s)
			return s
		}
		if v, ok := object["hash"]; ok {
			var entries [][]json.RawMessage
			json.Unmarshal(v, &entries)
			result := Variation{}
			for _, e := range entries {
				var key string
				json.Unmarshal(e[0], &key)
				result = append(result, Entry{key, typedValue(t, e[1])})
			}
			return result
		}
	}
	var array []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' {
		json.Unmarshal(raw, &array)
		result := []any{}
		for _, v := range array {
			result = append(result, typedValue(t, v))
		}
		return result
	}
	var n int64
	if string(raw) != "null" && json.Unmarshal(raw, &n) == nil {
		return n
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func TestVariationMarshalVectors(t *testing.T) {
	raw, err := os.ReadFile("../../reference/vectors/storage.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Variations []struct {
			Typed          json.RawMessage
			DecodedTyped   json.RawMessage `json:"decoded_typed"`
			Marshal        string          `json:"marshal_hex"`
			DecodedMarshal string          `json:"decoded_marshal_hex"`
			Digest         string
			DecodedDigest  string `json:"decoded_digest"`
		}
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	for i, v := range data.Variations {
		for _, c := range []struct {
			raw             json.RawMessage
			marshal, digest string
		}{{v.Typed, v.Marshal, v.Digest}, {v.DecodedTyped, v.DecodedMarshal, v.DecodedDigest}} {
			variation := typedValue(t, c.raw).(Variation)
			if got := hex.EncodeToString(variation.MarshalRuby()); got != c.marshal {
				t.Errorf("case %d Marshal: %s want %s", i, got, c.marshal)
			}
			if got := variation.Digest(); got != c.digest {
				t.Errorf("case %d digest %s want %s", i, got, c.digest)
			}
		}
	}
}
