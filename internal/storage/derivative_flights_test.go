package storage

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Block the processing boundary, not SQLite or the behavior being coordinated.
// Existing real-media tests protect the committed graph and owned-file lifecycle.
func TestDerivativeFlightCancellation(t *testing.T) {
	for _, who := range []string{"none", "leader", "waiter", "both"} {
		t.Run(who, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &Store{}
				leader, cancelLeader := context.WithCancel(context.Background())
				waiter, cancelWaiter := context.WithCancel(context.Background())
				defer cancelLeader()
				defer cancelWaiter()
				started, release := make(chan struct{}), make(chan struct{})
				var available atomic.Bool
				var created atomic.Int32
				lookup := func(context.Context) (database.Blob, error) {
					if available.Load() {
						return database.Blob{ID: 7}, nil
					}
					return database.Blob{}, sql.ErrNoRows
				}
				create := func(ctx context.Context) (database.Blob, error) {
					if created.Add(1) == 1 {
						close(started)
					}
					select {
					case <-ctx.Done():
						return database.Blob{}, ctx.Err()
					case <-release:
						available.Store(true)
						return database.Blob{ID: 7}, nil
					}
				}
				type result struct {
					name string
					blob database.Blob
					err  error
				}
				results := make(chan result, 2)
				run := func(name string, ctx context.Context) {
					blob, err := store.derivative(ctx, derivativeKey{blob: 1}, lookup, create)
					results <- result{name, blob, err}
				}
				go run("leader", leader)
				<-started
				go run("waiter", waiter)
				synctest.Wait() // Both callers are blocked inside the flight.
				if who == "leader" || who == "both" {
					cancelLeader()
				}
				if who == "waiter" || who == "both" {
					cancelWaiter()
				}
				synctest.Wait()
				close(release)
				synctest.Wait()
				for range 2 {
					got := <-results
					cancelled := who == "both" || got.name == who
					if cancelled {
						if !errors.Is(got.err, context.Canceled) {
							t.Errorf("%s cancellation: %v", got.name, got.err)
						}
					} else if got.err != nil || got.blob.ID != 7 {
						t.Errorf("live %s lost work: %+v", got.name, got)
					}
				}
				want := int32(1)
				if who == "leader" {
					want = 2 // The live waiter retries, rather than inheriting cancellation.
				}
				if created.Load() != want || len(store.derivatives.active) != 0 {
					t.Fatalf(
						"creations %d/%d, retained flights %d",
						created.Load(),
						want,
						len(store.derivatives.active),
					)
				}
			})
		})
	}
}
