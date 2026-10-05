package views

import "strings"

// The user view-model shared by every users/accounts/autocompletable template (the reference's
// users/summary.rs).

type Role uint8

const (
	RoleMember Role = iota
	RoleAdministrator
	RoleBot
)

func (r Role) String() string {
	switch r {
	case RoleAdministrator:
		return "administrator"
	case RoleBot:
		return "bot"
	}
	return "member"
}

type Status uint8

const (
	StatusActive Status = iota
	StatusDeactivated
	StatusBanned
)

// UserSummary is a User row as the views see it.
type UserSummary struct {
	ID           int64
	Name         string
	Bio          *string
	EmailAddress *string
	Role         Role
	Status       Status
	// fresh_user_avatar_path(user).
	AvatarPath string
}

func (u *UserSummary) Active() bool        { return u.Status == StatusActive }
func (u *UserSummary) Banned() bool        { return u.Status == StatusBanned }
func (u *UserSummary) Deactivated() bool   { return u.Status == StatusDeactivated }
func (u *UserSummary) Bot() bool           { return u.Role == RoleBot }
func (u *UserSummary) Administrator() bool { return u.Role == RoleAdministrator }

// Title is User#title.
func (u *UserSummary) Title() string { return UserTitle(u.Name, u.Bio) }

// Initials is User#initials.
func (u *UserSummary) Initials() string { return Initials(u.Name) }

func (u *UserSummary) Avatar() AvatarUser {
	return AvatarUser{ID: u.ID, Title: u.Title(), AvatarPath: u.AvatarPath}
}

// FirstName is name.split(' ')[0] (awk-style split on ASCII whitespace).
func (u *UserSummary) FirstName() string {
	if parts := u.NameParts(); len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// NameParts is name.split(' ').
func (u *UserSummary) NameParts() []string {
	return strings.FieldsFunc(u.Name, isASCIIWhitespace)
}

// isASCIIWhitespace is Rust's char::is_ascii_whitespace: space, tab, LF, FF and CR (not VT).
func isASCIIWhitespace(c rune) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}
