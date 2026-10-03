package rails

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// SignStream matches Turbo::StreamsChannel.signed_stream_name: JSON, SHA256, no purpose.
func (s *Secrets) SignStream(name string) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(name); err != nil {
		panic(err)
	}
	payload := base64.StdEncoding.EncodeToString(bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}))
	mac := hmac.New(sha256.New, s.streams)
	mac.Write([]byte(payload))
	return payload + "--" + hex.EncodeToString(mac.Sum(nil))
}
func (s *Secrets) VerifyStream(signed string) (string, error) {
	i := len(signed) - 66
	if i <= 0 || signed[i:i+2] != "--" {
		return "", ErrInvalid
	}
	mac := hmac.New(sha256.New, s.streams)
	mac.Write([]byte(signed[:i]))
	if !hmac.Equal([]byte(signed[i+2:]), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return "", ErrInvalid
	}
	data, err := decode64(signed[:i])
	if err != nil {
		return "", ErrInvalid
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err = decoder.Decode(&value); err != nil {
		return "", ErrInvalid
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	default:
		return "", ErrInvalid
	}
}
func RoomStream(kind string, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("gid://campfire/%s/%d", kind, id))) + ":messages"
}
func StreamRoom(name string) (string, int64, error) {
	gid, suffix, ok := strings.Cut(name, ":")
	if !ok || suffix != "messages" {
		return "", 0, ErrInvalid
	}
	decoded, err := decode64(gid)
	if err != nil {
		return "", 0, ErrInvalid
	}
	parts := strings.Split(string(decoded), "/")
	if len(parts) != 5 || parts[0] != "gid:" || parts[1] != "" || parts[2] != "campfire" {
		return "", 0, ErrInvalid
	}
	switch parts[3] {
	case "Room", "Rooms::Open", "Rooms::Closed", "Rooms::Direct":
	default:
		return "", 0, ErrInvalid
	}
	id, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || id <= 0 {
		return "", 0, ErrInvalid
	}
	return parts[3], id, nil
}

func UserRoomsStream(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("gid://campfire/User/%d", id))) + ":rooms"
}
