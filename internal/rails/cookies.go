// Package rails implements the persisted Rails wire formats used by Campfire.
package rails

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid signed or encrypted message")

// Secrets are derived once at boot, as in reference/crates/rails_compat/src/cookies.rs.
// Rails derives with SHA256 but signs cookies with SHA1.
type Secrets struct {
	secret     string
	signing    []byte
	signedIDs  []byte
	signedGIDs []byte
	streams    []byte
	encryption cipher.AEAD
	// fingerprint identifies the signing-key generation (SHA-256 of the key)
	// for caches that must never serve a value verified under another secret.
	fingerprint [8]byte
}

func DeriveKey(secret, salt string, length int) []byte {
	key, err := pbkdf2.Key(sha256.New, secret, []byte(salt), 1000, length)
	if err != nil {
		panic(err)
	}
	return key
}

func NewSecrets(secret string) (*Secrets, error) {
	if secret == "" {
		return nil, errors.New("SECRET_KEY_BASE is required")
	}
	block, err := aes.NewCipher(DeriveKey(secret, "authenticated encrypted cookie", 32))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Secrets{secret: secret, signedIDs: DeriveKey(secret, "active_record/signed_id", 64), signedGIDs: DeriveKey(secret, "signed_global_ids", 64), signing: DeriveKey(secret, "signed cookie", 64), encryption: aead, streams: DeriveKey(secret, "turbo/signed_stream_verifier_key", 64)}
	sum := sha256.Sum256(s.signing)
	s.fingerprint = [8]byte(sum[:8])
	return s, nil
}

// SigningFingerprint returns a stable identifier of the signing-key
// generation. It exists for caches keyed on secret changes: a value verified
// under one Secrets must not be served from a cache that now sees a different
// secret, and comparing fingerprints makes that rejection independent of which
// Secrets instance populated the cache.
func (s *Secrets) SigningFingerprint() [8]byte { return s.fingerprint }

func encode(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	// ActiveSupport escapes HTML characters but preserves Unicode line separators.
	// Decode string tokens and re-quote separators only, without changing literal \u text.
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	for i := 0; i < len(data); i++ {
		if data[i] == '\\' && i+1 < len(data) {
			if i+5 < len(data) && (string(data[i:i+6]) == "\\u2028" || string(data[i:i+6]) == "\\u2029") {
				if data[i+5] == '8' {
					out.WriteRune('\u2028')
				} else {
					out.WriteRune('\u2029')
				}
				i += 5
			} else {
				out.WriteByte(data[i])
				i++
				out.WriteByte(data[i])
			}
		} else {
			out.WriteByte(data[i])
		}
	}
	return []byte(out.String()), nil
}

// Struct field order is part of Rails' signed byte representation.
func envelope(value any, name string, expires time.Time) ([]byte, error) {
	data, err := encode(value)
	if err != nil {
		return nil, err
	}
	var expiry *string
	if !expires.IsZero() {
		s := expires.UTC().Format("2006-01-02T15:04:05.000Z")
		expiry = &s
	}
	return encode(struct {
		Rails struct {
			Message string  `json:"message"`
			Exp     *string `json:"exp"`
			Pur     string  `json:"pur"`
		} `json:"_rails"`
	}{struct {
		Message string  `json:"message"`
		Exp     *string `json:"exp"`
		Pur     string  `json:"pur"`
	}{base64.StdEncoding.EncodeToString(data), expiry, "cookie." + name}})
}

func decode64(s string) ([]byte, error) {
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(strings.NewReplacer("-", "+", "_", "/").Replace(s), "="))
}

// unpack decodes data into dest and returns the envelope's signed expiry (zero
// when the cookie carries none). Every check is unchanged from the previous
// behaviour; the expiry is returned so the auth fast path can cache a verified
// value and re-check it against the current time on later requests.
func unpack(data []byte, name string, now time.Time, dest any) (time.Time, error) {
	var obj map[string]json.RawMessage
	if strings.HasPrefix(string(data), `{"_rails":{"message":`) && json.Unmarshal(data, &obj) == nil && obj["_rails"] != nil {
		var meta struct {
			Message *string `json:"message"`
			Exp     *string `json:"exp"`
			Pur     *string `json:"pur"`
		}
		if json.Unmarshal(obj["_rails"], &meta) != nil || meta.Message == nil {
			return time.Time{}, ErrInvalid
		}
		if meta.Pur != nil && *meta.Pur != "" && *meta.Pur != "cookie."+name {
			return time.Time{}, ErrInvalid
		}
		if meta.Exp != nil {
			expiry, err := time.Parse(time.RFC3339Nano, *meta.Exp)
			if err != nil || !now.Before(expiry) {
				return time.Time{}, ErrInvalid
			}
			data, err = decode64(*meta.Message)
			if err != nil {
				return time.Time{}, ErrInvalid
			}
			if err := json.Unmarshal(data, dest); err != nil {
				return time.Time{}, ErrInvalid
			}
			return expiry, nil
		}
		var err error
		data, err = decode64(*meta.Message)
		if err != nil {
			return time.Time{}, ErrInvalid
		}
	}
	// Pre-metadata JSON cookies are accepted; Marshal is deliberately never decoded.
	if err := json.Unmarshal(data, dest); err != nil {
		return time.Time{}, ErrInvalid
	}
	return time.Time{}, nil
}

func (s *Secrets) SignCookie(name string, value any, expires time.Time) (string, error) {
	data, err := envelope(value, name, expires)
	if err != nil {
		return "", err
	}
	payload := base64.StdEncoding.EncodeToString(data)
	mac := hmac.New(sha1.New, s.signing)
	mac.Write([]byte(payload))
	return payload + "--" + hex.EncodeToString(mac.Sum(nil)), nil
}

func (s *Secrets) VerifyCookie(name, raw string, now time.Time, dest any) error {
	_, err := s.VerifyCookieExpires(name, raw, now, dest)
	return err
}

// VerifyCookieExpires verifies like VerifyCookie and additionally returns the
// envelope's signed expiry (zero when the cookie carries none), so callers can
// cache a verified value and re-check the expiry against a later time without
// repeating the HMAC and decode.
func (s *Secrets) VerifyCookieExpires(name, raw string, now time.Time, dest any) (time.Time, error) {
	i := len(raw) - 42
	if i <= 0 || raw[i:i+2] != "--" {
		return time.Time{}, ErrInvalid
	}
	payload, signature := raw[:i], raw[i+2:]
	mac := hmac.New(sha1.New, s.signing)
	mac.Write([]byte(payload))
	if !hmac.Equal([]byte(signature), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return time.Time{}, ErrInvalid
	}
	data, err := decode64(payload)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	return unpack(data, name, now, dest)
}

func (s *Secrets) EncryptCookie(name string, value any, expires time.Time) (string, error) {
	data, err := envelope(value, name, expires)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.encryption.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	encrypted := s.encryption.Seal(nil, nonce, data, nil)
	split := len(encrypted) - s.encryption.Overhead()
	b64 := base64.StdEncoding.EncodeToString
	return b64(encrypted[:split]) + "--" + b64(nonce) + "--" + b64(encrypted[split:]), nil
}

func (s *Secrets) DecryptCookie(name, raw string, now time.Time, dest any) error {
	parts := strings.Split(raw, "--")
	if len(parts) != 3 {
		return ErrInvalid
	}
	ciphertext, e1 := base64.StdEncoding.DecodeString(parts[0])
	nonce, e2 := base64.StdEncoding.DecodeString(parts[1])
	tag, e3 := base64.StdEncoding.DecodeString(parts[2])
	if e1 != nil || e2 != nil || e3 != nil || len(nonce) != s.encryption.NonceSize() || len(tag) != s.encryption.Overhead() {
		return ErrInvalid
	}
	data, err := s.encryption.Open(nil, nonce, append(ciphertext, tag...), nil)
	if err != nil {
		return ErrInvalid
	}
	_, err = unpack(data, name, now, dest)
	return err
}

func EscapeCookie(s string) string {
	// Rack allows '*' and escapes '~', unlike Go's QueryEscape.
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "~", "%7E"), "%2A", "*")
}
func UnescapeCookie(s string) string {
	v, err := url.QueryUnescape(s)
	if err != nil {
		return s
	}
	return v
}
