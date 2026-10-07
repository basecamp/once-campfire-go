package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
)

type requestHostKey struct{}

func (s *Server) mention(u database.User) richtext.Mention {
	title := u.Name
	if strings.TrimSpace(u.Bio) != "" {
		title += " – " + u.Bio
	}
	return richtext.Mention{ID: u.ID, Name: u.Name, Title: title, SGID: s.Secrets.SGID(fmt.Sprintf("gid://campfire/User/%d?expires_in", u.ID), "attachable", time.Time{}), Path: fmt.Sprintf("/users/%d", u.ID), Avatar: "/users/" + s.Secrets.SignedID("User", u.ID, "avatar", time.Time{}) + "/avatar?v=" + u.UpdatedAt.UTC().Format("20060102150405")}
}
func (s *Server) richContext(ctx context.Context) richtext.Context {
	host, _ := ctx.Value(requestHostKey{}).(string)
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	// The mention cache is sized on first use: bodies without attachments
	// (the overwhelming majority) never resolve a token, so the map — which
	// was allocated on every call before ENGINE-45b — only exists when a
	// mention actually asks for it.
	var cache map[int64]*richtext.Mention
	return richtext.Context{Host: host, Resolve: func(token string, verified bool) (*richtext.Mention, error) {
		var gid string
		var err error
		if verified {
			gid, err = s.Secrets.VerifySGID(token, "attachable", s.DB.Now())
			if err != nil {
				return nil, nil
			}
		} else {
			gid, err = rails.UnverifiedUserGID(token)
			if err != nil {
				return nil, err
			}
		}
		gid, _, _ = strings.Cut(gid, "?")
		parts := strings.Split(gid, "/")
		if len(parts) != 5 || parts[3] != "User" {
			return nil, nil
		}
		id, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil {
			return nil, nil
		}
		if cache == nil {
			cache = make(map[int64]*richtext.Mention)
		}
		if m, ok := cache[id]; ok {
			return m, nil
		}
		u, err := s.DB.User(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			cache[id] = nil
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		m := s.mention(u)
		cache[id] = &m
		return &m, nil
	}}
}
func (s *Server) richText(ctx context.Context, body string) richtext.Result {
	result, _ := richtext.Process(body, s.richContext(ctx))
	return result
}

// richPost carries every derived form of one posted body from a single parse
// (ENGINE-45b): the canonical stored body, the stored plain text, the
// presentation HTML the message view renders, and the mentioned user ids.
// createMessage computes it once per request instead of canonicalMessage,
// Display and MentionIDs each parsing the body again; each field is exactly
// what the corresponding focused call produces for the same input (the
// richtext oracle pins the equality).
type richPost struct {
	canonical    string
	plain        string
	presentation string
	mentioned    []int64
}

// richPostFor canonicalizes body and derives plain text, presentation and
// mentions from one parse of the canonical body with ctx.
func (s *Server) richPostFor(body string, ctx richtext.Context) *richPost {
	canonical := richtext.Canonical(body)
	result, _ := richtext.ProcessMessage(canonical, ctx)
	return &richPost{canonical: canonical, plain: result.Plain, presentation: result.Presentation, mentioned: result.Mentioned}
}

// richMentions returns the rich result's mentioned ids, or nil when there is
// no rich result (a post with no body parameter), so the push and webhook
// paths fall back to deriving them from the stored body exactly as they did
// before ENGINE-45b.
func richMentions(rich *richPost) []int64 {
	if rich == nil {
		return nil
	}
	return rich.mentioned
}

func (s *Server) canonicalMessage(ctx context.Context, body string) (string, string) {
	body = richtext.Canonical(body)
	plain, _ := richtext.PlainText(body, s.richContext(ctx))
	return body, plain
}

func (s *Server) plainText(ctx context.Context, body string) string {
	plain, _ := richtext.PlainText(body, s.richContext(ctx))
	return plain
}
func (s *Server) mentionedIDs(ctx context.Context, body string) []int64 {
	ids, _ := richtext.MentionIDs(body, s.richContext(ctx))
	return ids
}
