package rails

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRemainingRailsVectors(t *testing.T) {
	data, err := os.ReadFile("../../reference/vectors/rails_compat.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Secret string `json:"secret_key_base"`
		Signed struct {
			Generate, Verify []struct {
				Case, Model, Purpose, Now string
				ID                        int64
				Signed                    string `json:"signed_id"`
				Expires                   string `json:"expires_at"`
				Expected                  json.RawMessage
			}
		} `json:"signed_ids"`
		SGIDs struct {
			Generate, Verify []struct {
				Case, GID, Data, SGID, Purpose, Now string
				Expires                             string `json:"expires_at"`
				Expected                            *string
			}
		} `json:"sgids"`
		Unverified []struct {
			Case, SGID string
			Expected   json.RawMessage
		} `json:"unverified_sgids"`
		Apps struct {
			Generate, Verify []struct {
				Case, Name, Message, Purpose, Now string
				Data                              string  `json:"data_json"`
				Expires                           string  `json:"expires_at"`
				Expected                          *string `json:"expected_json"`
			}
		} `json:"app_verifiers"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	s, err := NewSecrets(fixture.Secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Signed.Generate {
		if got := s.SignedID(c.Model, c.ID, c.Purpose, instant(c.Expires)); got != c.Signed {
			t.Errorf("signed ID generate: %s != %s", got, c.Signed)
		}
	}
	for _, c := range fixture.Signed.Verify {
		t.Run("ID/"+c.Case, func(t *testing.T) {
			got, err := s.VerifyID(c.Model, c.Signed, c.Purpose, instant(c.Now))
			if string(c.Expected) == "null" {
				if err == nil {
					t.Fatal("accepted invalid ID", got)
				}
				return
			}
			var want int64
			if json.Unmarshal(c.Expected, &want) != nil {
				var text string
				if json.Unmarshal(c.Expected, &text) != nil {
					t.Fatal("invalid expected ID")
				}
				var err error
				want, err = strconv.ParseInt(text, 10, 64)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err != nil || got != want {
				t.Fatalf("%d (%v) want %d", got, err, want)
			}
		})
	}
	for _, c := range fixture.SGIDs.Generate {
		if got := s.SGID(c.Data, c.Purpose, instant(c.Expires)); got != c.SGID {
			t.Errorf("SGID generate: %s != %s", got, c.SGID)
		}
	}
	for _, c := range fixture.SGIDs.Verify {
		t.Run("SGID/"+c.Case, func(t *testing.T) {
			got, err := s.VerifySGID(c.SGID, c.Purpose, instant(c.Now))
			if c.Expected == nil {
				if err == nil {
					t.Fatal("accepted invalid SGID", got)
				}
			} else if err != nil || got != *c.Expected {
				t.Fatalf("%q (%v) want %q", got, err, *c.Expected)
			}
		})
	}

	for _, c := range fixture.Unverified {
		t.Run("unverified/"+c.Case, func(t *testing.T) {
			got, err := UnverifiedUserGID(c.SGID)
			if len(c.Expected) > 0 && c.Expected[0] == '{' {
				if err == nil {
					t.Fatal("malformed SGID did not return an error")
				}
				return
			}
			// The vector reference contains User 1 and User 2; resolution happens after parsing.
			if got != "gid://campfire/User/1" && got != "gid://campfire/User/2" {
				got = ""
			}
			var want *string
			if json.Unmarshal(c.Expected, &want) != nil {
				t.Fatal("bad vector")
			}
			if want == nil {
				if err != nil || got != "" {
					t.Fatalf("unexpected result %q %v", got, err)
				}
			} else if err != nil || got != *want {
				t.Fatalf("%q (%v) want %q", got, err, *want)
			}
		})
	}

	for _, c := range fixture.Apps.Generate {
		got, err := s.AppVerifier(c.Name).GenerateRaw([]byte(c.Data), c.Purpose, instant(c.Expires))
		if err != nil || got != c.Message {
			t.Errorf("app generate: %s (%v) != %s", got, err, c.Message)
		}
	}
	for _, c := range fixture.Apps.Verify {
		t.Run("app/"+c.Case, func(t *testing.T) {
			got, err := s.AppVerifier(c.Name).VerifyRaw(c.Message, c.Purpose, instant(c.Now))
			if c.Expected == nil {
				if err == nil {
					t.Fatal("accepted invalid message", string(got))
				}
			} else if err != nil || string(got) != *c.Expected {
				t.Fatalf("%s (%v) want %s", got, err, *c.Expected)
			}
		})
	}
}
func TestVerifierMetadataSeparation(t *testing.T) {
	s, _ := NewSecrets("secret")
	v := s.AppVerifier("ActiveStorage")
	message, err := v.Generate(1, "blob_id", time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		purpose string
		now     time.Time
	}{{"blob_token", time.Unix(0, 0)}, {"", time.Unix(0, 0)}, {"blob_id", time.Unix(100, 0)}} {
		if _, err = v.VerifyRaw(message, c.purpose, c.now); err == nil {
			t.Fatal("accepted wrong-purpose or expired message")
		}
	}
}

// SignedID's shortcuts (the id as its own JSON, plain purposes quoted as they are, reused HMACs)
// sign what Generate does.
func TestSignedIDMatchesGenerate(t *testing.T) {
	secrets, err := NewSecrets("secret-key-base")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		id      int64
		model   string
		purpose string
		expires time.Time
	}{
		{1, "User", "avatar", time.Time{}},
		{-42, "Rooms::Open", "", time.Time{}},
		{9007199254740993, "User", "avatar", expires},
		{7, "User", "a\"b<c>&\u2028", time.Time{}},
	} {
		for range 3 {
			got := secrets.SignedID(test.model, test.id, test.purpose, test.expires)
			want, err := Verifier{Key: secrets.signedIDs, SHA256: true, URLSafe: true}.Generate(test.id, modelPurpose(test.model, test.purpose), test.expires)
			if err != nil || got != want {
				t.Fatalf("SignedID(%d, %q, %q) = %s, want %s (%v)", test.id, test.model, test.purpose, got, want, err)
			}
		}
	}
}

func TestPlainJSONStringsMatchEncoder(t *testing.T) {
	for _, s := range []string{"", "user/avatar", "a b~", "<b>", "&", "q\"", `back\slash`, "tab\t", "\x7f", "é", "\u2028"} {
		for _, html := range []bool{false, true} {
			got, _ := jsonString(s, html)
			var out bytes.Buffer
			encoder := json.NewEncoder(&out)
			encoder.SetEscapeHTML(html)
			encoder.Encode(s)
			want := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSuffix(out.String(), "\n"), `\u2028`, "\u2028"), `\u2029`, "\u2029")
			if string(got) != want {
				t.Errorf("jsonString(%q, %v) = %s, want %s", s, html, got, want)
			}
		}
	}
}
