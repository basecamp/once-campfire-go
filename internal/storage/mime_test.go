package storage

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMarcelVectors(t *testing.T) {
	var vectors struct {
		Marcel []struct {
			Name     string
			DataHex  *string `json:"data_hex"`
			Fixture  string
			Declared *string `json:"declared_type"`
			Type     string  `json:"content_type"`
		}
	}
	raw, err := os.ReadFile("../../reference/vectors/storage.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors.Marcel {
		var data []byte
		if v.DataHex != nil {
			data, err = hex.DecodeString(*v.DataHex)
		} else {
			data, err = os.ReadFile(filepath.Join("../../reference/reference/test/fixtures/files", v.Fixture))
		}
		if err != nil {
			t.Fatal(err)
		}
		declared := ""
		if v.Declared != nil {
			declared = *v.Declared
		}
		if got := Identify(data, v.Name, declared); got != v.Type {
			t.Errorf("%s (%s): got %s want %s", v.Name, declared, got, v.Type)
		}
		prefix := data[:min(len(data), MagicPrefixLength)]
		if got := Identify(prefix, v.Name, declared); got != v.Type {
			t.Errorf("prefix %s: got %s want %s", v.Name, got, v.Type)
		}
	}
}
