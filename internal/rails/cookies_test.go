package rails

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
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

func TestVerifyCookieExpires(t *testing.T) {
	secrets, err := NewSecrets("expiry-test")
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	raw, err := secrets.SignCookie("session_token", "token-123", expiry)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	got, err := secrets.VerifyCookieExpires("session_token", raw, now, &token)
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-123" {
		t.Fatalf("token = %q", token)
	}
	if !got.Equal(expiry) {
		t.Fatalf("expiry = %v, want %v", got, expiry)
	}
	// VerifyCookie still behaves identically.
	if err := secrets.VerifyCookie("session_token", raw, now, &token); err != nil {
		t.Fatalf("VerifyCookie: %v", err)
	}
	// A cookie already expired at verification time fails like VerifyCookie.
	if _, err := secrets.VerifyCookieExpires("session_token", raw, expiry.Add(time.Second), &token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired cookie: got %v, want ErrInvalid", err)
	}
	// A cookie without an envelope expiry returns a zero expiry.
	noExpiry, err := secrets.SignCookie("session_token", "token-456", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	got, err = secrets.VerifyCookieExpires("session_token", noExpiry, now, &token)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Fatalf("expiry = %v, want zero", got)
	}
	// Tampered bytes never verify, in either shape.
	mutated := []byte(raw)
	mutated[len(mutated)/2] ^= 0x40
	if _, err := secrets.VerifyCookieExpires("session_token", string(mutated), now, &token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered cookie: got %v, want ErrInvalid", err)
	}
}

func TestSigningFingerprint(t *testing.T) {
	a, err := NewSecrets("same-secret")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSecrets("same-secret")
	if err != nil {
		t.Fatal(err)
	}
	if a.SigningFingerprint() != b.SigningFingerprint() {
		t.Fatal("fingerprints differ for the same secret")
	}
	c, err := NewSecrets("other-secret")
	if err != nil {
		t.Fatal(err)
	}
	if a.SigningFingerprint() == c.SigningFingerprint() {
		t.Fatal("fingerprints match for different secrets")
	}
}
