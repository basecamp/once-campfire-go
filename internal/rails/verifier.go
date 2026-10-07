package rails

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrPurpose = errors.New("message purpose mismatch")
var ErrExpired = errors.New("message expired")

// The two camel-to-snake passes modelPurpose runs are immutable, so their
// patterns compile once instead of on every signed id (avatars sign on every
// rendered message).
var (
	camelHeadPattern = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	camelMidPattern  = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

type Verifier struct {
	Key                                         []byte
	SHA256, URLSafe, Padded, HTML, AllowMarshal bool
}

func (s *Secrets) AppVerifier(name string) Verifier {
	return Verifier{Key: DeriveKey(s.secret, name, 64), HTML: true, AllowMarshal: true}
}
func (s *Secrets) idVerifier() Verifier {
	return Verifier{Key: s.signedIDs, SHA256: true, URLSafe: true}
}
func (s *Secrets) sgidVerifier() Verifier {
	return Verifier{Key: s.signedGIDs, URLSafe: true, Padded: true, HTML: true, AllowMarshal: true}
}
func (v Verifier) mac(data string) string {
	var algorithm func() hash.Hash = sha1.New
	if v.SHA256 {
		algorithm = sha256.New
	}
	mac := hmac.New(algorithm, v.Key)
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// CanonicalJSON preserves object insertion order and integer precision, unlike a map[string]any.
func CanonicalJSON(raw []byte, escapeHTML bool) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out bytes.Buffer
	var read func() error
	read = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case json.Delim:
			out.WriteByte(byte(value))
			first := true
			for decoder.More() {
				if !first {
					out.WriteByte(',')
				}
				first = false
				if value == '{' {
					key, err := decoder.Token()
					if err != nil {
						return err
					}
					quoted, err := jsonString(key.(string), escapeHTML)
					if err != nil {
						return err
					}
					out.Write(quoted)
					out.WriteByte(':')
				}
				if err := read(); err != nil {
					return err
				}
			}
			close, err := decoder.Token()
			if err != nil {
				return err
			}
			out.WriteByte(byte(close.(json.Delim)))
		case string:
			b, err := jsonString(value, escapeHTML)
			if err != nil {
				return err
			}
			out.Write(b)
		case json.Number:
			out.WriteString(value.String())
		case nil:
			out.WriteString("null")
		case bool:
			out.WriteString(strconv.FormatBool(value))
		default:
			return ErrInvalid
		}
		return nil
	}
	if err := read(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return out.Bytes(), nil
}
func jsonString(value string, escapeHTML bool) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(escapeHTML)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	raw := bytes.TrimSuffix(out.Bytes(), []byte{'\n'})
	result := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) {
			if i+5 < len(raw) && (string(raw[i:i+6]) == `\u2028` || string(raw[i:i+6]) == `\u2029`) {
				if raw[i+5] == '8' {
					result = append(result, []byte("\u2028")...)
				} else {
					result = append(result, []byte("\u2029")...)
				}
				i += 5
			} else {
				result = append(result, raw[i], raw[i+1])
				i++
			}
		} else {
			result = append(result, raw[i])
		}
	}
	return result, nil
}
func (v Verifier) Generate(value any, purpose string, expires time.Time) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return v.GenerateRaw(raw, purpose, expires)
}
func (v Verifier) GenerateRaw(raw []byte, purpose string, expires time.Time) (string, error) {
	data, err := CanonicalJSON(raw, v.HTML)
	if err != nil {
		return "", err
	}
	if purpose != "" || !expires.IsZero() {
		out := append([]byte(`{"_rails":{"data":`), data...)
		if !expires.IsZero() {
			expiry, _ := jsonString(expires.UTC().Format("2006-01-02T15:04:05.000Z"), v.HTML)
			out = append(out, []byte(`,"exp":`)...)
			out = append(out, expiry...)
		}
		if purpose != "" {
			p, _ := jsonString(purpose, v.HTML)
			out = append(out, []byte(`,"pur":`)...)
			out = append(out, p...)
		}
		data = append(out, '}', '}')
	}
	encoding := base64.StdEncoding
	if v.URLSafe {
		encoding = base64.RawURLEncoding
		if v.Padded {
			encoding = base64.URLEncoding
		}
	}
	payload := encoding.EncodeToString(data)
	return payload + "--" + v.mac(payload), nil
}
func (v Verifier) VerifyRaw(message, purpose string, now time.Time) (json.RawMessage, error) {
	length := 40
	if v.SHA256 {
		length = 64
	}
	i := len(message) - length - 2
	if i <= 0 || message[i:i+2] != "--" || !hmac.Equal([]byte(v.mac(message[:i])), []byte(message[i+2:])) {
		return nil, ErrInvalid
	}
	data, err := decode64(message[:i])
	if err != nil {
		return nil, ErrInvalid
	}
	return v.decode(data, purpose, now)
}
func (v Verifier) Verify(message, purpose string, now time.Time, dest any) error {
	data, err := v.VerifyRaw(message, purpose, now)
	if err != nil {
		return err
	}
	if json.Unmarshal(data, dest) != nil {
		return ErrInvalid
	}
	return nil
}
func (v Verifier) decode(data []byte, purpose string, now time.Time) (json.RawMessage, error) {
	if len(data) > 1 && data[0] == 4 && data[1] == 8 {
		if !v.AllowMarshal {
			return nil, ErrInvalid
		}
		value, ok := marshalString(data)
		if !ok {
			return nil, ErrInvalid
		}
		var err error
		data, err = jsonString(value, v.HTML)
		if err != nil {
			return nil, err
		}
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) == nil && envelope["_rails"] != nil {
		var meta struct {
			Data    json.RawMessage `json:"data"`
			Message *string         `json:"message"`
			Exp     *string         `json:"exp"`
			Pur     any             `json:"pur"`
		}
		if json.Unmarshal(envelope["_rails"], &meta) != nil {
			return nil, ErrInvalid
		}
		if meta.Exp != nil {
			expires, err := time.Parse(time.RFC3339Nano, *meta.Exp)
			if err != nil {
				return nil, ErrInvalid
			}
			if !now.Before(expires) {
				return nil, ErrExpired
			}
		}
		actual := ""
		if meta.Pur != nil {
			actual = fmt.Sprint(meta.Pur)
		}
		if actual != purpose {
			return nil, ErrPurpose
		}
		if meta.Message != nil {
			decoded, err := decode64(*meta.Message)
			if err != nil {
				return nil, ErrInvalid
			}
			return v.decode(decoded, "", now)
		}
		if meta.Data == nil {
			return json.RawMessage("null"), nil
		}
		return CanonicalJSON(meta.Data, v.HTML)
	}
	if purpose != "" {
		return nil, ErrPurpose
	}
	return CanonicalJSON(data, v.HTML)
}
func marshalString(data []byte) (string, bool) {
	if len(data) < 4 || data[0] != 4 || data[1] != 8 {
		return "", false
	}
	data = data[2:]
	if data[0] == 'I' {
		data = data[1:]
	}
	if len(data) < 2 || data[0] != '"' {
		return "", false
	}
	data = data[1:]
	first := int(int8(data[0]))
	data = data[1:]
	length := 0
	switch {
	case first == 0:
	case first > 4:
		length = first - 5
	case first > 0:
		if len(data) < first {
			return "", false
		}
		for i := 0; i < first; i++ {
			length |= int(data[i]) << (8 * i)
		}
		data = data[first:]
	default:
		return "", false
	}
	if length < 0 || length > len(data) {
		return "", false
	}
	return string(data[:length]), true
}
func modelPurpose(model, purpose string) string {
	model = strings.ReplaceAll(model, "::", "/")
	model = camelHeadPattern.ReplaceAllString(model, "${1}_${2}")
	model = camelMidPattern.ReplaceAllString(model, "${1}_${2}")
	model = strings.ToLower(strings.ReplaceAll(model, "-", "_"))
	if strings.TrimSpace(purpose) != "" {
		model += "/" + purpose
	}
	return model
}
func (s *Secrets) SignedID(model string, id int64, purpose string, expires time.Time) string {
	value, err := s.idVerifier().Generate(id, modelPurpose(model, purpose), expires)
	if err != nil {
		panic(err)
	}
	return value
}
func (s *Secrets) VerifyID(model, message, purpose string, now time.Time) (int64, error) {
	v := s.idVerifier()
	raw, err := v.VerifyRaw(message, modelPurpose(model, purpose), now)
	if err != nil && !errors.Is(err, ErrPurpose) && !errors.Is(err, ErrExpired) {
		v.SHA256 = false
		v.AllowMarshal = true
		raw, err = v.VerifyRaw(message, modelPurpose(model, purpose), now)
	}
	if err != nil {
		return 0, err
	}
	var value string
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &value) != nil {
			return 0, ErrInvalid
		}
	} else {
		value = string(raw)
	}
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, ErrInvalid
	}
	return id, nil
}
func (s *Secrets) SGID(gid, purpose string, expires time.Time) string {
	signed, err := s.sgidVerifier().Generate(gid, purpose, expires)
	if err != nil {
		panic(err)
	}
	return signed
}
func (s *Secrets) VerifySGID(message, purpose string, now time.Time) (string, error) {
	v := s.sgidVerifier()
	var gid string
	if err := v.Verify(message, purpose, now, &gid); err == nil {
		return gid, nil
	}
	var old struct {
		GID     string  `json:"gid"`
		Purpose string  `json:"purpose"`
		Expires *string `json:"expires_at"`
	}
	if err := v.Verify(message, "", now, &old); err != nil {
		return "", err
	}
	if old.Purpose != purpose {
		return "", ErrPurpose
	}
	if old.Expires != nil {
		expires, err := time.Parse(time.RFC3339Nano, *old.Expires)
		if err != nil {
			return "", ErrInvalid
		}
		if now.After(expires) {
			return "", ErrExpired
		}
	}
	return old.GID, nil
}

// UnverifiedUserGID is the deliberately User-only exception in rails_ext/action_text_attachables.rb.
func UnverifiedUserGID(sgid string) (string, error) {
	if strings.Trim(sgid, "-") == "" {
		return "", nil
	}
	payload, _, _ := strings.Cut(sgid, "--")
	raw, err := decode64(payload)
	if err != nil {
		return "", ErrInvalid
	}
	var outer struct {
		Rails struct {
			Data    any     `json:"data"`
			Message *string `json:"message"`
		} `json:"_rails"`
	}
	if json.Unmarshal(raw, &outer) != nil {
		return "", ErrInvalid
	}
	var gid string
	if outer.Rails.Data != nil {
		gid, _ = outer.Rails.Data.(string)
	} else if outer.Rails.Message != nil {
		decoded, err := decode64(*outer.Rails.Message)
		if err != nil {
			return "", ErrInvalid
		}
		gid = regexp.MustCompile(`gid://campfire/[^/]+/\d+`).FindString(string(decoded))
	}
	if !strings.HasPrefix(gid, "gid://") {
		decoded, err := decode64(gid)
		if err != nil {
			return "", nil
		}
		gid = string(decoded)
	}
	gid, _, _ = strings.Cut(gid, "?")
	parts := strings.Split(gid, "/")
	if len(parts) != 5 || parts[0] != "gid:" || parts[2] == "" || parts[3] != "User" || parts[4] == "" {
		return "", nil
	}
	return "gid://campfire/User/" + parts[4], nil
}
