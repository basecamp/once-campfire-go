package views

// Views for reference/app/views/autocompletable (the reference's autocompletable.rs).

// AutocompletableUserJSON is autocompletable/users/_user.json.jbuilder; index.json.jbuilder is a
// JSON array of these.
type AutocompletableUserJSON struct {
	// h(user.name): HTML-escaped.
	Name  string `json:"name"`
	Value int64  `json:"value"`
	// fresh_user_avatar_url(user): absolute.
	AvatarURL string `json:"avatar_url"`
	Sgid      string `json:"sgid"`
}

func NewAutocompletableUserJSON(user *MentionUser, baseURL string) AutocompletableUserJSON {
	return AutocompletableUserJSON{
		Name:      Escape(user.Name),
		Value:     user.ID,
		AvatarURL: baseURL + user.AvatarPath,
		Sgid:      user.AttachableSgid,
	}
}

// AutocompletableUsersIndexJSON is autocompletable/users/index.json.jbuilder.
func AutocompletableUsersIndexJSON(users []MentionUser, baseURL string) string {
	json := make([]AutocompletableUserJSON, len(users))
	for i := range users {
		json[i] = NewAutocompletableUserJSON(&users[i], baseURL)
	}
	return railsJSON(json)
}
