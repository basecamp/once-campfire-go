package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Continuations are process-owned, not crash-durable. A nil candidate has yet to
// remove its graph; a receipt retains the key after that graph has disappeared.
// Serialize traversal so concurrent retries cannot drop another caller's state.
type purgeWork struct {
	registration uint64
	candidates   map[int64]*database.BlobRemoval
}

type purgeContinuations struct {
	once    sync.Once
	mu      sync.Mutex
	gate    chan struct{}
	pending map[int64]*purgeWork
}

func (p *purgeContinuations) register(root int64) *purgeWork {
	p.once.Do(func() {
		p.gate = make(chan struct{}, 1)
		p.pending = make(map[int64]*purgeWork)
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	work := p.pending[root]
	if work == nil {
		work = &purgeWork{candidates: map[int64]*database.BlobRemoval{root: nil}}
		p.pending[root] = work
	}
	work.registration++
	return work
}

// Complete runs under the traversal gate. A reference check can predate another
// detach/admission; it must not erase that newer root registration on retirement.
func (p *purgeContinuations) complete(root int64, work *purgeWork, registration uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending[root] != work {
		return
	}
	if work.registration != registration {
		if _, retained := work.candidates[root]; !retained {
			work.candidates[root] = nil
		}
	}
	if len(work.candidates) == 0 {
		delete(p.pending, root)
	}
}

func (p *purgeContinuations) acquire(ctx context.Context) error {
	select {
	case p.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RetainPurges records candidates before background admission. Queued work may be
// rejected or cancelled before executing; the storage owner still retains every
// root and can resume it with Purge. Like unlink continuations, this is not durable
// across process exit.
func (s *Store) RetainPurges(ids []int64) {
	for _, id := range ids {
		s.purges.register(id)
	}
}

// RetryPurges resumes process-owned roots, including tasks cancelled before
// execution and unlink continuations whose rows no longer exist.
func (s *Store) RetryPurges(ctx context.Context) error {
	p := &s.purges
	p.mu.Lock()
	roots := make([]int64, 0, len(p.pending))
	for root := range p.pending {
		roots = append(roots, root)
	}
	p.mu.Unlock()
	var failures error
	for _, root := range roots {
		failures = errors.Join(failures, s.Purge(ctx, root))
	}
	return failures
}

// Purge checks incoming references transactionally at each destructive step.
// Failed unlinks and unvisited descendants remain owned for retries by the root
// ID, even after its row is gone. Independent descendants continue on failure.
func (s *Store) Purge(ctx context.Context, root int64) error {
	p := &s.purges
	// Retain admission before waiting: an expired caller cannot erase the root.
	continuation := p.register(root)
	if err := p.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-p.gate }()
	p.mu.Lock()
	registration := continuation.registration
	p.mu.Unlock()
	work := continuation.candidates
	frontier := make([]int64, 0, len(work))
	for id := range work {
		frontier = append(frontier, id)
	}
	seen := make(map[int64]bool)
	var failures error
	for len(frontier) > 0 {
		id := frontier[0]
		frontier = frontier[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		removal := work[id]
		if removal == nil {
			receipt, err := s.DB.RemoveBlob(ctx, id)
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
			if receipt.ID == 0 {
				delete(work, id)
				continue
			}
			removal = &receipt
			work[id] = removal
		}
		// Transfer the frontier before touching bytes, not after unlink succeeds.
		for _, child := range removal.Descendants {
			if !seen[child] {
				if _, exists := work[child]; !exists {
					work[child] = nil
				}
				frontier = append(frontier, child)
			}
		}
		path, err := s.Path(removal.Key)
		if err == nil {
			err = os.Remove(path)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		if err == nil {
			err = os.RemoveAll(filepath.Join(s.Root, "variants", removal.Key))
		}
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		delete(work, id)
	}
	p.complete(root, continuation, registration)
	return failures
}
