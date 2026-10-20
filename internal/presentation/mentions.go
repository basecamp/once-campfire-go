package presentation

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
)

// MentionTarget distinguishes malformed content from a missing user. Resolving
// data is a preparation responsibility; this package never queries persistence.
type MentionTarget struct {
	ID    int64
	Error error
}

func MentionTargetFor(token string) MentionTarget {
	id, err := rails.UnverifiedUserID(token)
	return MentionTarget{ID: id, Error: err}
}

func Mention(secrets *rails.Secrets, u database.UserDisplay) richtext.Mention {
	title := u.Name
	if strings.TrimSpace(u.Bio) != "" {
		title += " – " + u.Bio
	}
	return richtext.Mention{ID: u.ID, Name: u.Name, Title: title, SGID: secrets.SGID(fmt.Sprintf("gid://campfire/User/%d?expires_in", u.ID), "attachable", time.Time{}), Path: fmt.Sprintf("/users/%d", u.ID), Avatar: "/users/" + secrets.SignedID("User", u.ID, "avatar", time.Time{}) + "/avatar?v=" + u.UpdatedAt.UTC().Format("20060102150405")}
}

// VerifiedMentionID preserves recipient resolution of both modern and legacy
// signed global IDs. Display's unverified exception is a separate contract.
func VerifiedMentionID(secrets *rails.Secrets, token string, now time.Time) int64 {
	gid, err := secrets.VerifySGID(token, "attachable", now)
	if err != nil {
		return 0
	}
	return rails.UserGIDID(gid)
}

// MentionContext consumes complete owned targets and explicit presentation facts.
// Its resolver retains no request, context, clock or database callback.
func MentionContext(secrets *rails.Secrets, host string, now time.Time, targets map[string]MentionTarget, users map[int64]database.UserDisplay) richtext.Context {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	// Capture user facts now, but sign/format only mentions actually requested.
	// Contexts can be reused concurrently, so each lazy value is synchronized.
	resolved := make(map[int64]func() *richtext.Mention, len(users))
	for id, user := range users {
		resolved[id] = sync.OnceValue(func() *richtext.Mention {
			mention := Mention(secrets, user)
			return &mention
		})
	}
	return richtext.Context{Host: host, Resolve: func(token string, verified bool) (*richtext.Mention, error) {
		target := targets[token]
		id, err := target.ID, target.Error
		if verified {
			id, err = VerifiedMentionID(secrets, token, now), nil
		}
		if resolve := resolved[id]; resolve != nil {
			return resolve(), err
		}
		return nil, err
	}}
}
