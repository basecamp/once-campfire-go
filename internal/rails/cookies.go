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
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
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
	// Reused HMACs (pooledMAC): SHA-256 with signedIDs, SHA-1 with signing.
	idMACs, cookieMACs sync.Pool
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
	return &Secrets{secret: secret, signedIDs: DeriveKey(secret, "active_record/signed_id", 64), signedGIDs: DeriveKey(secret, "signed_global_ids", 64), signing: DeriveKey(secret, "signed cookie", 64), encryption: aead, streams: DeriveKey(secret, "turbo/signed_stream_verifier_key", 64)}, nil
}

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

// urlSafeToStandard turns URL-safe Base64 into the standard alphabet.
var urlSafeToStandard = strings.NewReplacer("-", "+", "_", "/")

func decode64(s string) ([]byte, error) {
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(urlSafeToStandard.Replace(s), "="))
}

func unpack(data []byte, name string, now time.Time, dest any) error {
	var obj map[string]json.RawMessage
	if strings.HasPrefix(string(data), `{"_rails":{"message":`) && json.Unmarshal(data, &obj) == nil && obj["_rails"] != nil {
		var meta struct {
			Message *string `json:"message"`
			Exp     *string `json:"exp"`
			Pur     *string `json:"pur"`
		}
		if json.Unmarshal(obj["_rails"], &meta) != nil || meta.Message == nil {
			return ErrInvalid
		}
		if meta.Pur != nil && *meta.Pur != "" && *meta.Pur != "cookie."+name {
			return ErrInvalid
		}
		if meta.Exp != nil {
			expiry, err := time.Parse(time.RFC3339Nano, *meta.Exp)
			if err != nil || !now.Before(expiry) {
				return ErrInvalid
			}
		}
		var err error
		data, err = decode64(*meta.Message)
		if err != nil {
			return ErrInvalid
		}
	}
	// Pre-metadata JSON cookies are accepted; Marshal is deliberately never decoded.
	if err := json.Unmarshal(data, dest); err != nil {
		return ErrInvalid
	}
	return nil
}

func (s *Secrets) SignCookie(name string, value any, expires time.Time) (string, error) {
	data, err := envelope(value, name, expires)
	if err != nil {
		return "", err
	}
	payload := base64.StdEncoding.EncodeToString(data)
	return payload + "--" + s.cookieMAC(payload), nil
}

// cookieMAC is the hex HMAC-SHA1 of a signed cookie's payload.
func (s *Secrets) cookieMAC(payload string) string {
	return pooledMAC(&s.cookieMACs, sha1.New, s.signing, payload)
}

func (s *Secrets) VerifyCookie(name, raw string, now time.Time, dest any) error {
	i := len(raw) - 42
	if i <= 0 || raw[i:i+2] != "--" {
		return ErrInvalid
	}
	payload, signature := raw[:i], raw[i+2:]
	if !hmac.Equal([]byte(signature), []byte(s.cookieMAC(payload))) {
		return ErrInvalid
	}
	data, err := decode64(payload)
	if err != nil {
		return ErrInvalid
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
	return unpack(data, name, now, dest)
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
