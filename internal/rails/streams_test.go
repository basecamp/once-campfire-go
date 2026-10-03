package rails

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestStreamVectors(t *testing.T) {
	data, err := os.ReadFile("../../reference/vectors/rails_compat.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Secret  string `json:"secret_key_base"`
		Streams struct {
			Generate []struct {
				Parts  []string
				Signed string
			}
			Verify []struct {
				Case, Signed string
				Expected     any
			}
		} `json:"turbo_stream_names"`
	}
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	s, err := NewSecrets(v.Secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Streams.Generate {
		if got := s.SignStream(strings.Join(c.Parts, ":")); got != c.Signed {
			t.Errorf("stream signature %q != %q", got, c.Signed)
		}
	}
	for _, c := range v.Streams.Verify {
		t.Run(c.Case, func(t *testing.T) {
			got, err := s.VerifyStream(c.Signed)
			if c.Expected == nil {
				if err == nil {
					t.Fatal("accepted invalid stream", got)
				}
			} else if err != nil || got != fmt.Sprint(c.Expected) {
				t.Fatalf("got %q (%v) want %v", got, err, c.Expected)
			}
		})
	}
}
func TestStreamRoom(t *testing.T) {
	for _, kind := range []string{"Rooms::Open", "Rooms::Closed", "Rooms::Direct"} {
		got, id, err := StreamRoom(RoomStream(kind, 42))
		if err != nil || got != kind || id != 42 {
			t.Fatal(got, id, err)
		}
	}
	if _, _, err := StreamRoom(RoomStream("User", 42)); err == nil {
		t.Fatal("user stream treated as room")
	}
}
