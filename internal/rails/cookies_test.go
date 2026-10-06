package rails

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type cookieCase struct {
	Case, Name, Raw, Now, ExpiresAt string
	Value, Expected                 any
}

func (c *cookieCase) UnmarshalJSON(b []byte) error {
	type alias cookieCase
	var v struct {
		alias
		Expires string `json:"expires_at"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*c = cookieCase(v.alias)
	c.ExpiresAt = v.Expires
	return nil
}
func instant(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }
func TestRailsCookieVectors(t *testing.T) {
	data, err := os.ReadFile("../../reference/vectors/rails_compat.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Secret string `json:"secret_key_base"`
		Now    string
		Keys   []struct {
			Salt   string
			Length int
			Key    string `json:"key_hex"`
		} `json:"key_generator"`
		Signed    struct{ Generate, Verify []cookieCase } `json:"signed_cookies"`
		Encrypted struct{ Generate, Verify []cookieCase } `json:"encrypted_cookies"`
		Escaping  []struct {
			Raw          *string
			Wire, Parsed string
		} `json:"cookie_escaping"`
	}
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	secrets, err := NewSecrets(v.Secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Keys {
		t.Run("derive/"+c.Salt, func(t *testing.T) {
			if got := hex.EncodeToString(DeriveKey(v.Secret, c.Salt, c.Length)); got != c.Key {
				t.Fatalf("got %s want %s", got, c.Key)
			}
		})
	}
	for _, c := range v.Escaping {
		if c.Raw != nil && EscapeCookie(*c.Raw) != c.Wire {
			t.Errorf("escape %q", *c.Raw)
		}
		if UnescapeCookie(c.Wire) != c.Parsed {
			t.Errorf("unescape %q", c.Wire)
		}
	}
	for _, c := range v.Signed.Generate {
		t.Run("sign/"+c.Name, func(t *testing.T) {
			got, err := secrets.SignCookie(c.Name, c.Value, instant(c.ExpiresAt))
			if err != nil || got != c.Raw {
				t.Fatalf("got %s (%v) want %s", got, err, c.Raw)
			}
		})
	}
	for _, suite := range []struct {
		name  string
		cases []cookieCase
		read  func(string, string, time.Time, any) error
	}{
		{"signed", v.Signed.Verify, secrets.VerifyCookie}, {"encrypted", v.Encrypted.Verify, secrets.DecryptCookie},
	} {
		for _, c := range suite.cases {
			t.Run(suite.name+"/"+c.Case, func(t *testing.T) {
				var got any
				err := suite.read(c.Name, c.Raw, instant(c.Now), &got)
				if c.Expected == nil {
					if err == nil && got != nil {
						t.Fatalf("accepted invalid cookie: %v", got)
					}
					return
				}
				if err != nil || !reflect.DeepEqual(got, c.Expected) {
					t.Fatalf("got %#v (%v) want %#v", got, err, c.Expected)
				}
			})
		}
	}
	for _, c := range v.Encrypted.Generate {
		t.Run("encrypt/"+c.Name, func(t *testing.T) {
			raw, err := secrets.EncryptCookie(c.Name, c.Value, instant(c.ExpiresAt))
			if err != nil {
				t.Fatal(err)
			}
			var got any
			if err = secrets.DecryptCookie(c.Name, raw, instant(v.Now), &got); err != nil || !reflect.DeepEqual(got, c.Value) {
				t.Fatalf("got %#v (%v)", got, err)
			}
			raw = "!" + raw[1:]
			if secrets.DecryptCookie(c.Name, raw, instant(v.Now), &got) == nil {
				t.Fatal("accepted tampered ciphertext")
			}
		})
	}
}

// Signed cookies sign and verify the same with reused HMACs as with fresh ones.
func TestSignedCookieMACsAreReusable(t *testing.T) {
	secrets, err := NewSecrets("secret-key-base")
	if err != nil {
		t.Fatal(err)
	}
	first, err := secrets.SignCookie("session_token", "abc", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		again, _ := secrets.SignCookie("session_token", "abc", time.Time{})
		if again != first {
			t.Fatalf("signed again: %s, first %s", again, first)
		}
		var value string
		if err := secrets.VerifyCookie("session_token", again, time.Now(), &value); err != nil || value != "abc" {
			t.Fatalf("verify: %q %v", value, err)
		}
	}
	tampered := first[:len(first)-1] + string("0123456789abcdef"[(strings.IndexByte("0123456789abcdef", first[len(first)-1])+1)%16])
	var value string
	if err := secrets.VerifyCookie("session_token", tampered, time.Now(), &value); err == nil {
		t.Fatal("a tampered cookie verified")
	}
}
