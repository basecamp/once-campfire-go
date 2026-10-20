package application

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/basecamp/once-campfire-go/internal/jobs"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

// Cleanup owns detached identities through admission, including a saturated queue.
type Cleanup struct {
	Storage *storage.Store
	Jobs    *jobs.Runner
}

func (s *Cleanup) Detached(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	ids = slices.Clone(ids)
	s.Storage.RetainPurges(ids)
	purge := purgeTask{store: s.Storage, ids: ids}
	if s.Jobs.Enqueue(purge) == jobs.Accepted {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return purge.Run(ctx)
}

type purgeTask struct {
	store *storage.Store
	ids   []int64
}

func (purgeTask) Queue() string { return "purge" }
func (task purgeTask) Run(ctx context.Context) error {
	var failures error
	for _, id := range task.ids {
		failures = errors.Join(failures, task.store.Purge(ctx, id))
	}
	return failures
}
