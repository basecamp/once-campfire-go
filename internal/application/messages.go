// Package application coordinates authoritative commands and their post-commit work.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/jobs"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

type Messages struct {
	DB      *database.DB
	Storage *storage.Store
	Jobs    *jobs.Runner
	Cleanup *Cleanup
}

// MessageResult distinguishes a failed command from processing after its commit.
// A non-nil command error means no MessageCommit; Processing never undoes it.
type MessageResult struct {
	Commit     database.MessageCommit
	Processing error
}

func (s *Messages) Create(ctx context.Context, actor, room int64, client string, body *string, upload *storage.Staged) (MessageResult, error) {
	return s.create(ctx, actor, room, client, body, upload, false)
}

func (s *Messages) Reply(ctx context.Context, bot, room int64, body *string, upload *storage.Staged) (MessageResult, error) {
	return s.create(ctx, bot, room, "", body, upload, true)
}

func (s *Messages) create(ctx context.Context, actor, room int64, client string, body *string, upload *storage.Staged, reply bool) (MessageResult, error) {
	input := database.MessageInput{ClientID: client, Body: body}
	if upload != nil {
		defer upload.Discard()
		input.Upload = &upload.Blob
	}
	var commit database.MessageCommit
	var err error
	if reply {
		commit, err = s.DB.CreateWebhookReply(ctx, actor, room, input)
	} else {
		commit, err = s.DB.CreateMessage(ctx, actor, room, input)
	}
	if err != nil {
		return MessageResult{}, err
	}
	result := MessageResult{Commit: commit}
	if upload != nil {
		upload.Commit(*commit.Uploaded)
		// This committed obligation stays joined to the command, but a posting
		// disconnect must not abandon it. Processing owns its whole-pipeline bound.
		_, result.Processing = s.Storage.ProcessAttachment(context.WithoutCancel(ctx), upload.Blob)
	}
	return result, nil
}

func (s *Messages) Delete(ctx context.Context, actor, id int64) (MessageResult, error) {
	commit, err := s.DB.DeleteMessage(ctx, actor, id)
	if err != nil {
		return MessageResult{}, err
	}
	return MessageResult{Commit: commit, Processing: s.Cleanup.Detached(commit.Detached)}, nil
}

func (s *Messages) RemoveBanned(ctx context.Context, id int64) (MessageResult, error) {
	commit, err := s.DB.RemoveBannedMessage(ctx, id)
	if err != nil {
		return MessageResult{}, err
	}
	return MessageResult{Commit: commit, Processing: s.Cleanup.Detached(commit.Detached)}, nil
}

func (s *Messages) Update(ctx context.Context, actor, id int64, body *string, attachment *int64, upload *storage.Staged) (MessageResult, error) {
	input := database.MessageInput{Body: body, Attachment: attachment}
	if upload != nil {
		defer upload.Discard()
		input.Upload = &upload.Blob
	}
	commit, err := s.DB.UpdateMessage(ctx, actor, id, input)
	if err != nil {
		return MessageResult{}, err
	}
	result := MessageResult{Commit: commit}
	if upload != nil {
		upload.Commit(*commit.Uploaded)
	}
	result.Processing = s.Cleanup.Detached(commit.Detached)
	if upload != nil {
		analyze := analyzeTask{db: s.DB, store: s.Storage, id: commit.AttachmentID}
		if s.Jobs.Enqueue(analyze) != jobs.Accepted {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			result.Processing = errors.Join(result.Processing, analyze.Run(ctx))
			cancel()
		}
	}
	return result, nil
}
