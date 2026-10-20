// Package jobs runs the bounded, independent queues used by the Rust port.
package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type (
	// Task owns application meaning; Runner owns only bounded scheduling.
	Task interface {
		Queue() string
		Run(context.Context) error
	}
	Admission string
	Runner    struct {
		mu      sync.RWMutex
		closed  bool
		pending int
		changed chan struct{}
		done    chan struct{}
		queues  map[string]chan Task
		joined  sync.Once
		ctx     context.Context
		cancel  context.CancelFunc
		workers sync.WaitGroup
	}
)

const (
	Accepted Admission = "accepted"
	Closed   Admission = "closed"
	Full     Admission = "full"
)

func New(concurrency int, kinds ...string) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{
		changed: make(chan struct{}, 1),
		done:    make(chan struct{}),
		queues:  map[string]chan Task{},
		ctx:     ctx,
		cancel:  cancel,
	}
	for _, kind := range kinds {
		queue := make(chan Task, 1024)
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

func run(ctx context.Context, kind string, work Task) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("background job panic", "kind", kind, "error", p)
		}
	}()
	if err := work.Run(ctx); err != nil {
		slog.Error("background job failed", "kind", kind, "error", err)
	}
}

func (r *Runner) Enqueue(work Task) Admission {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Closed
	}
	kind := work.Queue()
	queue, ok := r.queues[kind]
	if !ok {
		panic("unregistered job kind: " + kind)
	}
	select {
	case queue <- work:
		r.pending++
		return Accepted
	default:
		return Full
	}
}

func (r *Runner) join() {
	r.workers.Wait()
	r.joined.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		// Cancellation can leave queued owned payloads. After joining, neither
		// workers nor admission can access them; release their retained memory.
		clear(r.queues)
		r.pending = 0
	})
}

// The HTTP server stops accepting requests before Close. Queued work can still
// enqueue dependent work (a banned message's attachment purge, for example).
// changed wakes one drain waiter; done broadcasts cancellation/drain initiation.
// A timeout cancels work; every caller still joins the workers before returning.
// A process deadline must force exit rather than close resources under stuck work.
func (r *Runner) Close(timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			r.join()
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
			r.join()
			return
		}
		r.mu.Unlock()
		select {
		case <-r.changed:
		case <-r.done:
			r.join()
			return
		case <-timer.C:
			r.mu.Lock()
			if r.closed {
				r.mu.Unlock()
				r.join()
				return
			}
			r.closed = true
			close(r.done)
			r.mu.Unlock()
			r.cancel()
			slog.Warn("background jobs cancelled at shutdown")
			r.join()
			return
		}
	}
}
