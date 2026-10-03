package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/basecamp/once-campfire-go/internal/richtext"
	xhtml "golang.org/x/net/html"
)

var unfurlSlots = make(chan struct{}, 16)
var parseSlots = make(chan struct{}, 4)
var mediaURL = regexp.MustCompile(`\bhttps?://\S+\.(?:zip|tar|tar\.gz|tar\.bz2|tar\.xz|gz|bz2|rar|7z|dmg|exe|msi|pkg|deb|iso|jpg|jpeg|png|gif|bmp|mp4|mov|avi|mkv|wmv|flv|heic|heif|mp3|wav|ogg|aac|wma|webm|ogv|mpg|mpeg)\b`)

type Unfurler struct {
	Client   *http.Client
	Resolver Resolver
}

func NewUnfurler() *Unfurler {
	return &Unfurler{HTTPClient(true, 5*time.Second, nil), net.DefaultResolver}
}
func (u *Unfurler) fetch(ctx context.Context, method, location string) ([]byte, string, error) {
	for range 10 {
		parsed, err := url.Parse(location)
		if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, "", ErrPrivate
		}
		request, err := http.NewRequestWithContext(ctx, method, location, nil)
		if err != nil {
			return nil, "", err
		}
		request.Header.Set("Accept", "*/*")
		request.Header.Set("User-Agent", "Ruby")
		response, err := u.Client.Do(request)
		if err != nil {
			return nil, "", err
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			location = response.Header.Get("Location")
			response.Body.Close()
			continue
		}
		contentType := response.Header.Get("Content-Type")
		if method == "HEAD" {
			response.Body.Close()
			return nil, contentType, nil
		}
		media := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
		if response.StatusCode != 200 || media != "text/html" || response.ContentLength > 5<<20 {
			response.Body.Close()
			return nil, "", nil
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, (5<<20)+1))
		response.Body.Close()
		if err != nil {
			return nil, "", err
		}
		if len(body) > 5<<20 {
			return nil, "", nil
		}
		return body, contentType, nil
	}
	return nil, "", errors.New("too many Open Graph redirects")
}
func openGraphAttributes(body []byte) map[string]string {
	var decoded strings.Builder
	for len(body) > 0 {
		r, n := utf8.DecodeRune(body)
		if r == utf8.RuneError && n == 1 {
			r = rune(body[0])
		}
		decoded.WriteRune(r)
		body = body[n:]
	}
	text := strings.SplitN(decoded.String(), "\x00", 2)[0]
	z := xhtml.NewTokenizer(strings.NewReader(text))
	metas := []map[string]string{}
	hasCharset := false
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		if tt != xhtml.StartTagToken && tt != xhtml.SelfClosingTagToken {
			continue
		}
		token := z.Token()
		if token.Data != "meta" {
			continue
		}
		attrs := map[string]string{}
		for _, a := range token.Attr {
			attrs[a.Key] = a.Val
		}
		if strings.TrimSpace(attrs["charset"]) != "" || strings.EqualFold(attrs["http-equiv"], "content-type") && strings.Contains(strings.ToLower(attrs["content"]), "charset=") {
			hasCharset = true
		}
		metas = append(metas, attrs)
	}
	found := map[string]string{}
	for _, m := range metas {
		if !strings.HasPrefix(m["property"], "og:") && !strings.HasPrefix(m["name"], "og:") {
			continue
		}
		key, has := m["property"]
		if !has {
			key = m["name"]
		}
		key = strings.ReplaceAll(key, "og:", "")
		if key != "title" && key != "url" && key != "image" && key != "description" {
			continue
		}
		content := m["content"]
		if strings.TrimSpace(content) == "" {
			continue
		}
		if !hasCharset {
			content = strings.Map(func(c rune) rune {
				if c > 127 {
					return -1
				}
				return c
			}, content)
		}
		found[key] = content
	}
	return found
}
func (u *Unfurler) Unfurl(ctx context.Context, location string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case unfurlSlots <- struct{}{}:
		defer func() { <-unfurlSlots }()
	case <-ctx.Done():
		return nil, nil
	}
	parsed, err := url.Parse(location)
	if err == nil && parsed.Scheme == "mailto" && !strings.Contains(parsed.Opaque, "@") {
		return nil, errors.New("URI invalid component")
	}
	for _, c := range location {
		if c > 127 || c <= 32 {
			return nil, nil
		}
	}
	if err != nil {
		return nil, nil
	}
	fetchURL := location
	tweet := false
	switch parsed.Hostname() {
	case "twitter.com", "www.twitter.com", "x.com", "www.x.com":
		if parsed.Path != "" && parsed.Path != "/" {
			tweet = true
			copy := *parsed
			copy.Host = "fxtwitter.com"
			fetchURL = copy.String()
		}
	}
	var body []byte
	if !mediaURL.MatchString(fetchURL) {
		body, _, _ = u.fetch(ctx, "GET", fetchURL)
	}
	if ctx.Err() != nil {
		return nil, nil
	}
	if tweet && body == nil {
		return nil, errors.New("missing Twitter Open Graph document")
	}
	select {
	case parseSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, nil
	}
	found := openGraphAttributes(body)
	<-parseSlots
	canonical := found["url"]
	if !PublicURL(ctx, u.Resolver, canonical) {
		canonical = location
	}
	var image *string
	if value := found["image"]; strings.TrimSpace(value) != "" {
		_, ct, err := u.fetch(ctx, "HEAD", value)
		if err == nil {
			switch strings.ToLower(ct) {
			case "image/jpeg", "image/png", "image/gif", "image/webp":
				if PublicURL(ctx, u.Resolver, value) {
					image = &value
				}
			}
		}
	}
	title, err := richtext.StripTags(found["title"])
	if err != nil {
		return nil, err
	}
	description, err := richtext.StripTags(found["description"])
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil || strings.TrimSpace(title) == "" || strings.TrimSpace(description) == "" || strings.TrimSpace(canonical) == "" {
		return nil, nil
	}
	// Preserve the reference model's attribute insertion order in render json:.
	values := map[string]any{"title": title, "url": canonical, "image": image, "description": description}
	keys := []string{}
	for _, key := range []string{"title", "url", "image", "description"} {
		if _, ok := found[key]; ok {
			keys = append(keys, key)
		}
	}
	for _, key := range []string{"url", "image", "title", "description"} {
		if _, ok := found[key]; !ok {
			keys = append(keys, key)
		}
	}
	var result bytes.Buffer
	result.WriteByte('{')
	for _, key := range keys {
		encoded, _ := json.Marshal(values[key])
		result.WriteString(`"` + key + `":`)
		result.Write(encoded)
		result.WriteByte(',')
	}
	result.WriteString(`"context_for_validation":{"context":null},"errors":{}}`)
	return result.Bytes(), nil
}
