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
	cache := map[int64]*richtext.Mention{}
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
