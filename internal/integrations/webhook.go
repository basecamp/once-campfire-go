package integrations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrWebhookMIME = errors.New("invalid webhook response MIME type")
var ErrWebhookSize = errors.New("webhook reply exceeds 100 MB")

const MaxWebhookReply = 100 << 20

type WebhookReply struct {
	Status                int
	Text                  *string
	Attachment            []byte
	Filename, ContentType string
}
type WebhookClient struct{ Client *http.Client }

func NewWebhookClient() *WebhookClient {
	return &WebhookClient{Client: WebhookHTTPClient(7*time.Second, nil)}
}
func (c *WebhookClient) Deliver(ctx context.Context, endpoint string, payload []byte) (WebhookReply, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	reply, err := c.deliver(ctx, endpoint, payload)
	if err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			seconds := 7
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				seconds = 60
			}
			text := fmt.Sprintf("Failed to respond within %d seconds", seconds)
			return WebhookReply{Text: &text}, nil
		}
	}
	return reply, err
}
func (c *WebhookClient) deliver(ctx context.Context, endpoint string, payload []byte) (WebhookReply, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
		return WebhookReply{}, errors.New("invalid webhook URL")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return WebhookReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "Ruby")
	response, err := c.Client.Do(req)
	if err != nil {
		return WebhookReply{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxWebhookReply+1))
	if err != nil {
		return WebhookReply{}, err
	}
	if len(body) > MaxWebhookReply {
		return WebhookReply{}, ErrWebhookSize
	}
	reply := WebhookReply{Status: response.StatusCode}
	headers, ok := response.Header["Content-Type"]
	if !ok {
		return reply, nil
	}
	contentType := responseMediaType(strings.Join(headers, ", "))
	if response.StatusCode == 200 && (contentType == "text/plain" || contentType == "text/html") {
		text := strings.ToValidUTF8(string(body), "�")
		reply.Text = &text
		return reply, nil
	}
	symbol, registered, err := webhookMIME(contentType)
	if err != nil {
		return WebhookReply{}, err
	}
	reply.Attachment = body
	reply.Filename = "attachment." + symbol
	reply.ContentType = registered
	return reply, nil
}
func responseMediaType(header string) string {
	parts := strings.Split(strings.SplitN(header, ";", 2)[0], "/")
	strip := func(s string) string { return strings.Trim(s, " \t\n\v\f\r\x00") }
	main := strip(parts[0])
	if len(parts) > 1 {
		return main + "/" + strip(parts[1])
	}
	return main
}

var mimeName = `[a-zA-Z0-9][a-zA-Z0-9!#$&\-^_.+]{0,126}`
var webhookMIMEPattern = regexp.MustCompile(`^(?:\*/\*|` + mimeName + `/(?:\*|` + mimeName + `))$`)

func webhookMIME(value string) (string, string, error) {
	if registered, ok := webhookTypes[value]; ok {
		return registered[0], registered[1], nil
	}
	value = strings.TrimRight(strings.SplitN(value, ";", 2)[0], " \t\n\v\f\r\x00")
	if registered, ok := webhookTypes[value]; ok {
		return registered[0], registered[1], nil
	}
	if !webhookMIMEPattern.MatchString(value) {
		return "", "", ErrWebhookMIME
	}
	return "", value, nil
}
