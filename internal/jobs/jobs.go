// Package jobs runs the bounded, independent queues used by the Rust port.
package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Work func(context.Context) error
type Runner struct {
	mu      sync.RWMutex
	closed  bool
	pending int
	changed chan struct{}
	done    chan struct{}
	queues  map[string]chan Work
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
}

func New(concurrency int, kinds ...string) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{changed: make(chan struct{}, 1), done: make(chan struct{}), queues: map[string]chan Work{}, ctx: ctx, cancel: cancel}
	for _, kind := range kinds {
		queue := make(chan Work, 1024)
		r.queues[kind] = queue
		for range max(1, concurrency) {
			r.workers.Go(func() {
				for {
					select {
					case <-ctx.Done():
						return
					case work, ok := <-queue:
						if !ok {
							return
						}
						run(ctx, kind, work)
						r.mu.Lock()
						r.pending--
						r.mu.Unlock()
						select {
						case r.changed <- struct{}{}:
						default:
						}
					}
				}
			})
		}
	}
	return r
}
func run(ctx context.Context, kind string, work Work) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("background job panic", "kind", kind, "error", p)
		}
	}()
	if err := work(ctx); err != nil {
		slog.Error("background job failed", "kind", kind, "error", err)
	}
}
func (r *Runner) Enqueue(kind string, work Work) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	queue, ok := r.queues[kind]
	if !ok {
		panic("unregistered job kind: " + kind)
	}
	select {
	case queue <- work:
		r.pending++
		return true
	default:
		slog.Error("background job queue full; job dropped", "kind", kind)
		return false
	}
}

// The HTTP server stops accepting requests before Close. Queued work can still
// enqueue dependent work (a banned message's attachment purge, for example).
// changed wakes one drain waiter; done broadcasts shutdown to all closers.
func (r *Runner) Close(timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return
		}
		if r.pending == 0 {
			r.closed = true
			close(r.done)
			for _, queue := range r.queues {
				close(queue)
			}
			r.mu.Unlock()
			r.cancel()
			r.workers.Wait()
			return
		}
		r.mu.Unlock()
		select {
		case <-r.changed:
		case <-r.done:
			return
		case <-timer.C:
			r.mu.Lock()
			if r.closed {
				r.mu.Unlock()
				return
			}
			r.closed = true
			close(r.done)
			r.mu.Unlock()
			r.cancel()
			slog.Warn("background jobs abandoned at shutdown")
			return
		}
	}
}
