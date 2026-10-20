package application

import (
	"context"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
)

type AccountQueries struct {
	DB      *database.DB
	Secrets *rails.Secrets
}

type Suggestions struct {
	Mentions []richtext.Mention
	Count    int
	NextPage int64
}

// Suggest retains the room precheck before querying candidates, then materializes
// only the selected page's signed mention inputs outside the SQL lifetime.
func (q *AccountQueries) Suggest(ctx context.Context, viewer int64, room *int64, query string, number int64) (Suggestions, error) {
	id := int64(0)
	if room != nil {
		id = *room
		if _, err := q.DB.Room(ctx, viewer, id); err != nil {
			return Suggestions{}, err
		}
	}
	users, err := q.DB.AutocompleteUsers(ctx, id, query)
	if err != nil {
		return Suggestions{}, err
	}
	number = max(1, min(number, 1_000_000_000))
	data := Suggestions{Count: len(users)}
	if number != max(1, int64((len(users)+19)/20)) {
		data.NextPage = number + 1
	}
	offset := min(int64(len(users)), (number-1)*20)
	for _, user := range users[offset:min(int64(len(users)), offset+20)] {
		data.Mentions = append(data.Mentions, presentation.Mention(q.Secrets, user))
	}
	return data, nil
}

type AccountPeople struct {
	Administrators, Users []database.AccountMember
	NextPage              int64
}

func accountPage(number int64, count int) (int64, int64) {
	number = max(1, min(number, 1_000_000_000))
	last := int64(max(1, (count+499)/500))
	next := number + 1
	if number == last {
		next = 0
	}
	return number, next
}

func (q *AccountQueries) Settings(ctx context.Context, role int, number int64) (AccountPeople, error) {
	users, err := q.DB.AccountUsers(ctx, role == 1)
	if err != nil {
		return AccountPeople{}, err
	}
	var data AccountPeople
	_, data.NextPage = accountPage(number, len(users))
	for _, user := range users {
		if user.Role == 1 {
			data.Administrators = append(data.Administrators, user)
		} else {
			data.Users = append(data.Users, user)
		}
	}
	return data, nil
}

func (q *AccountQueries) Members(ctx context.Context, number int64) (AccountPeople, error) {
	users, err := q.DB.AccountUsers(ctx, false)
	if err != nil {
		return AccountPeople{}, err
	}
	number, next := accountPage(number, len(users))
	start := min(int((number-1)*500), len(users))
	return AccountPeople{Users: users[start:min(start+500, len(users))], NextPage: next}, nil
}
