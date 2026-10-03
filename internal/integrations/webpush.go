package integrations

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

func decode64(s string) ([]byte, error) {
	return base64.RawURLEncoding.Strict().DecodeString(strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(s), "="))
}
func encode64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func derive(salt, key, info []byte, n int) ([]byte, error) {
	return hkdf.Key(sha256.New, key, salt, string(info), n)
}
func pushKeys(shared, clientPublic, serverPublic, auth, salt []byte) (key, nonce []byte, err error) {
	info := append([]byte("WebPush: info\x00"), clientPublic...)
	info = append(info, serverPublic...)
	prk, err := derive(auth, shared, info, 32)
	if err != nil {
		return nil, nil, err
	}
	key, err = derive(salt, prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	if err != nil {
		return nil, nil, err
	}
	nonce, err = derive(salt, prk, []byte("Content-Encoding: nonce\x00"), 12)
	return
}
func encryptPushWith(message []byte, p256dh, auth string, server *ecdh.PrivateKey, salt, padding []byte, recordSize uint32) ([]byte, error) {
	if len(message) == 0 || p256dh == "" || auth == "" {
		return nil, errors.New("blank push encryption argument")
	}
	if len(message)+len(padding)+16 > 4096 {
		return nil, errors.New("encrypted payload is too big")
	}
	clientBytes, err := decode64(p256dh)
	if err != nil {
		return nil, err
	}
	clientBytes = bytes.TrimLeft(clientBytes, "\x00")
	pointBytes := clientBytes
	if len(clientBytes) == 33 {
		x, y := elliptic.UnmarshalCompressed(elliptic.P256(), clientBytes)
		if x == nil {
			return nil, ErrPushPoint
		}
		pointBytes = elliptic.Marshal(elliptic.P256(), x, y)
	}
	client, err := ecdh.P256().NewPublicKey(pointBytes)
	if err != nil {
		return nil, ErrPushPoint
	}
	secret, err := decode64(auth)
	if err != nil {
		return nil, err
	}
	shared, err := server.ECDH(client)
	if err != nil {
		return nil, err
	}
	serverPublic := server.PublicKey().Bytes()
	key, nonce, err := pushKeys(shared, clientBytes, serverPublic, secret, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain := append(bytes.Clone(message), padding...)
	encrypted := gcm.Seal(nil, nonce, plain, nil)
	if recordSize == 0 {
		recordSize = uint32(len(encrypted))
	}
	out := append([]byte{}, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(serverPublic)))
	out = append(out, serverPublic...)
	return append(out, encrypted...), nil
}
func EncryptPush(message []byte, p256dh, auth string) ([]byte, error) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptPushWith(message, p256dh, auth, key, salt, []byte{2, 0}, 0)
}

type VAPID struct {
	Subject string
	key     *ecdsa.PrivateKey
	public  []byte
}

func NewVAPID(subject, public, private string) (*VAPID, error) {
	raw, err := decode64(private)
	if err != nil || len(raw) > 32 {
		return nil, errors.New("invalid VAPID private key")
	}
	scalar := make([]byte, 32)
	copy(scalar[32-len(raw):], raw)
	ec, err := ecdh.P256().NewPrivateKey(scalar)
	if err != nil {
		return nil, err
	}
	pub, err := decode64(public)
	if err != nil {
		return nil, err
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), pub)
	if x == nil {
		x, y = elliptic.UnmarshalCompressed(elliptic.P256(), pub)
	}
	if x == nil || !bytes.Equal(elliptic.Marshal(elliptic.P256(), x, y), ec.PublicKey().Bytes()) {
		return nil, errors.New("VAPID keys do not match")
	}
	key := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: new(big.Int).SetBytes(scalar)}
	return &VAPID{subject, key, pub}, nil
}
func (v *VAPID) Authorization(audience string, now time.Time) (string, error) {
	claims, _ := json.Marshal(struct {
		Audience string `json:"aud"`
		Expires  int64  `json:"exp"`
		Subject  string `json:"sub"`
	}{audience, now.Add(12 * time.Hour).Unix(), v.Subject})
	signing := encode64([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + encode64(claims)
	hash := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, v.key, hash[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return "vapid t=" + signing + "." + encode64(signature) + ",k=" + encode64(v.public), nil
}
func (v *VAPID) PublicKey() string { return base64.URLEncoding.EncodeToString(v.public) }
