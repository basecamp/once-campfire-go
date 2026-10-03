package integrations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrPushGone = errors.New("push subscription is no longer valid")
var ErrPushPoint = errors.New("invalid push subscription P-256 point")
var pushHosts = []string{"jmt17.google.com", "fcm.googleapis.com", "updates.push.services.mozilla.com", "web.push.apple.com", "notify.windows.com"}

func PushEndpoint(value string) (*url.URL, error) {
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return nil, errors.New("invalid endpoint URL")
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Port() != "" && u.Port() != "443" {
		return nil, errors.New("push endpoint must use HTTPS on port 443")
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range pushHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return u, nil
		}
	}
	return nil, errors.New("endpoint is not a permitted push service")
}

type PushSender struct {
	Client   *http.Client
	Resolver Resolver
	VAPID    *VAPID
}

func NewPushSender(vapid *VAPID) *PushSender {
	return &PushSender{HTTPClient(true, 10*time.Second, nil), net.DefaultResolver, vapid}
}
func (p *PushSender) Validate(ctx context.Context, endpoint string) error {
	u, err := PushEndpoint(endpoint)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = ResolvePublic(ctx, p.Resolver, u.Hostname())
	return err
}
func (p *PushSender) Send(ctx context.Context, endpoint, key, auth string, message []byte) error {
	if p.VAPID == nil {
		return errors.New("Web Push is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u, err := PushEndpoint(endpoint)
	if err != nil {
		return nil
	}
	if _, err = ResolvePublic(ctx, p.Resolver, u.Hostname()); err != nil {
		return nil
	}
	payload, err := EncryptPush(message, key, auth)
	if err != nil {
		return err
	}
	authorization, err := p.VAPID.Authorization("https://"+u.Hostname(), time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "2419200")
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", authorization)
	req.Header.Set("User-Agent", "Ruby")
	req.Header.Set("Accept", "*/*")
	response, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == 404 || response.StatusCode == 410 {
		return ErrPushGone
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("push service returned %d", response.StatusCode)
	}
	return nil
}

// The limits count JSON string contents, matching Rust's notification truncation.
func TruncatePush(text string, limit int) string {
	size := func(r rune) int {
		switch r {
		case '"', '\\', '\n', '\r', '\t', '\b', '\f':
			return 2
		}
		if r < 32 {
			return 6
		}
		return utf8.RuneLen(r)
	}
	total := 0
	for _, r := range text {
		total += size(r)
	}
	if total <= limit {
		return text
	}
	used := len("…")
	for i, r := range text {
		used += size(r)
		if used > limit {
			return text[:i] + "…"
		}
	}
	return text
}
