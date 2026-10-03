package integrations

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"encoding/json"
	"os"
	"testing"
)

func TestWebPushRFC8291(t *testing.T) {
	b64 := func(s string) []byte {
		v, e := decode64(s)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	key, err := ecdh.P256().NewPrivateKey(b64("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := encryptPushWith(b64("V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24"), "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4", "BTBZMqHH6r4Tts7J_aSIgg", key, b64("DGv6ra1nlYgDCS1FRnbzlw"), []byte{2}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if encode64(actual) != want {
		t.Fatal("RFC encryption mismatch")
	}
}
func TestDecryptReferenceWebPush(t *testing.T) {
	raw, err := os.ReadFile("../../reference/crates/campfire/src/integrations/testdata/web_push_expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]json.RawMessage
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	text := func(k string) string { var s string; json.Unmarshal(v[k], &s); return s }
	b64 := func(s string) []byte {
		b, e := decode64(s)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	receiver, err := ecdh.P256().NewPrivateKey(b64(text("receiver_private_key")))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"reference", "go"} {
		var body []byte
		if kind == "reference" {
			body = b64(text("ciphertext"))
		} else {
			body, err = EncryptPush([]byte(text("message")), text("p256dh"), text("auth"))
			if err != nil {
				t.Fatal(err)
			}
		}
		salt := body[:16]
		n := int(body[20])
		pub := body[21 : 21+n]
		server, err := ecdh.P256().NewPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		shared, err := receiver.ECDH(server)
		if err != nil {
			t.Fatal(err)
		}
		key, nonce, err := pushKeys(shared, receiver.PublicKey().Bytes(), pub, b64(text("auth")), salt)
		if err != nil {
			t.Fatal(err)
		}
		block, _ := aes.NewCipher(key)
		gcm, _ := cipher.NewGCM(block)
		plain, err := gcm.Open(nil, nonce, body[21+n:], nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(plain, append([]byte(text("message")), 2, 0)) {
			t.Fatalf("%s decryption mismatch", kind)
		}
	}
}
