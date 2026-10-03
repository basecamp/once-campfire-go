package integrations

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPushDelivery(t *testing.T) {
	key, _ := ecdh.P256().GenerateKey(rand.Reader)
	vapid, err := NewVAPID("mailto:test@example.com", encode64(key.PublicKey().Bytes()), encode64(key.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	clientKey, _ := ecdh.P256().GenerateKey(rand.Reader)
	for _, status := range []int{201, 404, 410, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			sender := NewPushSender(vapid)
			sender.Resolver = &oracleDNS{map[string][][]string{"fcm.googleapis.com": {{"8.8.8.8"}}}, map[string]int{}}
			sender.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" || r.Header.Get("TTL") != "2419200" || r.Header.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
					t.Fatalf("request headers: %v", r.Header)
				}
				body, _ := io.ReadAll(r.Body)
				if len(body) < 100 {
					t.Fatal("missing encrypted record")
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			err := sender.Send(context.Background(), "https://fcm.googleapis.com/send/test", encode64(clientKey.PublicKey().Bytes()), encode64(make([]byte, 16)), []byte(`{"title":"Hi"}`))
			if (err == nil) != (status == 201) {
				t.Fatalf("status %d: %v", status, err)
			}
			if errors.Is(err, ErrPushGone) != (status == 404 || status == 410) {
				t.Fatalf("invalidated status %d: %v", status, err)
			}
		})
	}
}
func TestPushEndpointAndTruncation(t *testing.T) {
	for _, s := range []string{"http://fcm.googleapis.com/x", "https://fcm.googleapis.com:444/x", "https://fcm.googleapis.com.attacker.test/x", "https://localhost/x", "https://fcm.googleapis.com/a b"} {
		if _, err := PushEndpoint(s); err == nil {
			t.Errorf("allowed %q", s)
		}
	}
	for _, s := range []string{"https://fcm.googleapis.com/x", "https://a.notify.windows.com:443/x"} {
		if _, err := PushEndpoint(s); err != nil {
			t.Errorf("rejected %q", s)
		}
	}
	for _, c := range []struct {
		in, out string
		n       int
	}{{"short", "short", 5}, {"longer", "lo…", 5}, {"😀😀", "😀…", 7}, {strings.Repeat("\x01", 10), "\x01\x01…", 16}, {`"""`, `"…`, 5}} {
		if got := TruncatePush(c.in, c.n); got != c.out {
			t.Errorf("%q => %q, want %q", c.in, got, c.out)
		}
	}
}
