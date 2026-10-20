package application

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/jobs"
)

type Accounts struct {
	DB           *database.DB
	Attachments  *Attachments
	Messages     *Messages
	Publications *MessagePublications
	Cable        *cable.Hub
	Jobs         *jobs.Runner
}

type UserResult struct {
	Commit     database.UserCommit
	Processing error
}
type AccountResult struct {
	Commit     database.AccountCommit
	Processing error
}
type AttachmentResult struct {
	Commit     database.AttachmentCommit
	Processing error
}
type UserStateResult struct {
	ID         int64
	Banned     bool
	Processing error
}

func (s *Accounts) DeleteAvatar(ctx context.Context, actor int64) (AttachmentResult, error) {
	commit, err := s.DB.DeleteAvatar(ctx, actor)
	if err != nil {
		return AttachmentResult{}, err
	}
	return AttachmentResult{Commit: commit, Processing: s.Attachments.Cleanup.Detached(commit.Detached)}, nil
}

func (s *Accounts) DeleteLogo(ctx context.Context, actor int64) (AttachmentResult, error) {
	commit, err := s.DB.DeleteLogo(ctx, actor)
	if err != nil {
		return AttachmentResult{}, err
	}
	return AttachmentResult{Commit: commit, Processing: s.Attachments.Cleanup.Detached(commit.Detached)}, nil
}

func (s *Accounts) Setup(ctx context.Context, name, email, password string, avatar Attachment) (UserResult, error) {
	defer avatar.Discard()
	commit, err := s.DB.Setup(ctx, name, email, password, avatar.input())
	if err != nil {
		return UserResult{}, err
	}
	return UserResult{Commit: commit, Processing: s.Attachments.committed(avatar, commit.AttachmentCommit)}, nil
}

func (s *Accounts) Join(ctx context.Context, code, ip string, input database.UserInput, avatar Attachment) (UserResult, error) {
	defer avatar.Discard()
	input.Avatar = avatar.input()
	commit, err := s.DB.JoinUser(ctx, code, ip, input)
	if err != nil {
		return UserResult{}, err
	}
	return UserResult{Commit: commit, Processing: s.Attachments.committed(avatar, commit.AttachmentCommit)}, nil
}

func (s *Accounts) CreateUser(ctx context.Context, actor int64, input database.UserInput, avatar Attachment) (UserResult, error) {
	defer avatar.Discard()
	input.Avatar = avatar.input()
	commit, err := s.DB.CreateUser(ctx, actor, input)
	if err != nil {
		return UserResult{}, err
	}
	return UserResult{Commit: commit, Processing: s.Attachments.committed(avatar, commit.AttachmentCommit)}, nil
}

func (s *Accounts) UpdateUser(ctx context.Context, actor, id int64, input database.UserChanges, avatar Attachment) (UserResult, error) {
	return s.updateUser(ctx, actor, id, input, avatar, false)
}

func (s *Accounts) UpdateBot(ctx context.Context, actor, id int64, input database.UserChanges, avatar Attachment) (UserResult, error) {
	return s.updateUser(ctx, actor, id, input, avatar, true)
}

func (s *Accounts) updateUser(ctx context.Context, actor, id int64, input database.UserChanges, avatar Attachment, bot bool) (UserResult, error) {
	defer avatar.Discard()
	input.Avatar = avatar.input()
	var commit database.UserCommit
	var err error
	if bot {
		commit, err = s.DB.UpdateBot(ctx, actor, id, input)
	} else {
		commit, err = s.DB.UpdateUser(ctx, actor, id, input)
	}
	if err != nil {
		return UserResult{}, err
	}
	return UserResult{Commit: commit, Processing: s.Attachments.committed(avatar, commit.AttachmentCommit)}, nil
}

func (s *Accounts) UpdateAccount(ctx context.Context, actor int64, input database.AccountInput, logo Attachment) (AccountResult, error) {
	defer logo.Discard()
	input.Logo = logo.input()
	commit, err := s.DB.UpdateAccount(ctx, actor, input)
	if err != nil {
		return AccountResult{}, err
	}
	return AccountResult{Commit: commit, Processing: s.Attachments.committed(logo, commit.AttachmentCommit)}, nil
}

func (s *Accounts) Deactivate(ctx context.Context, actor, id int64) error {
	if err := s.DB.DeactivateUser(ctx, actor, id); err != nil {
		return err
	}
	s.Cable.Disconnect(id)
	return nil
}

func (s *Accounts) DeactivateBot(ctx context.Context, actor, id int64) error {
	if err := s.DB.DeactivateBot(ctx, actor, id); err != nil {
		return err
	}
	s.Cable.Disconnect(id)
	return nil
}

// Ban's command error means precommit failure. Processing identifies a rejected
// cleanup continuation without disguising the committed status/session removal.
func (s *Accounts) Ban(ctx context.Context, actor, id int64, ban bool) (UserStateResult, error) {
	if err := s.DB.BanUser(ctx, actor, id, ban); err != nil {
		return UserStateResult{}, err
	}
	s.Cable.Disconnect(id)
	result := UserStateResult{ID: id, Banned: ban}
	if !ban {
		return result, nil
	}
	remove := banTask{accounts: s, userID: id}
	if s.Jobs.Enqueue(remove) == jobs.Accepted {
		return result, nil
	}
	fallback, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result.Processing = remove.Run(fallback)
	return result, nil
}

type banTask struct {
	accounts *Accounts
	userID   int64
}

func (banTask) Queue() string { return "ban" }
func (task banTask) Run(ctx context.Context) error {
	messages, err := task.accounts.DB.MessagesByCreator(ctx, task.userID)
	if err != nil {
		return err
	}
	var failures error
	for _, message := range messages {
		result, err := task.accounts.Messages.RemoveBanned(ctx, message.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		_, processing := task.accounts.Publications.Removed(ctx, result)
		failures = errors.Join(failures, processing)
	}
	return failures
}
