package storage

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// A blank digest identifies a preview; variant digests are always nonempty.
type derivativeKey struct {
	blob   int64
	digest string
}

type derivativeFlight struct {
	done      chan struct{}
	err       error
	cancelled bool
}

type derivativeFlights struct {
	mu     sync.Mutex
	active map[derivativeKey]*derivativeFlight
}

// Waiters share only completion, never a Blob's mutable bytes. Each successful
// waiter reads its own committed record. The leader owns processing with its
// original context: cancelling it stops that work, then live waiters retry with
// their own contexts. There is no detached work or independently owned lifetime.
func (s *Store) derivative(
	ctx context.Context,
	key derivativeKey,
	lookup func(context.Context) (database.Blob, error),
	create func(context.Context) (database.Blob, error),
) (database.Blob, error) {
	for {
		if err := ctx.Err(); err != nil {
			return database.Blob{}, err
		}
		if existing, err := lookup(ctx); !errors.Is(err, sql.ErrNoRows) {
			return existing, err
		}
		flights := &s.derivatives
		flights.mu.Lock()
		if flight := flights.active[key]; flight != nil {
			flights.mu.Unlock()
			select {
			case <-ctx.Done():
				return database.Blob{}, ctx.Err()
			case <-flight.done:
			}
			if err := ctx.Err(); err != nil {
				return database.Blob{}, err
			}
			if flight.err != nil && !flight.cancelled {
				return database.Blob{}, flight.err
			}
			continue
		}
		// Unique cold requests retain the existing media-slot bound. Admission
		// bounds this optimization's bookkeeping, not application availability.
		if len(flights.active) >= 16 {
			flights.mu.Unlock()
			return create(ctx)
		}
		if flights.active == nil {
			flights.active = make(map[derivativeKey]*derivativeFlight)
		}
		flight := &derivativeFlight{done: make(chan struct{})}
		flights.active[key] = flight
		flights.mu.Unlock()

		return func() (blob database.Blob, err error) {
			finished := false
			defer func() {
				flights.mu.Lock()
				flight.err, flight.cancelled = err, ctx.Err() != nil
				if !finished {
					// Do not swallow the leader's panic or strand its waiters.
					flight.err = errors.New("derivative processing panicked")
				}
				delete(flights.active, key)
				close(flight.done)
				flights.mu.Unlock()
			}()
			// Another process or a previous leader may have committed since the
			// initial lookup; database state remains authoritative.
			blob, err = lookup(ctx)
			if errors.Is(err, sql.ErrNoRows) {
				blob, err = create(ctx)
			}
			finished = true
			return blob, err
		}()
	}
}
