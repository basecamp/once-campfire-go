package jobs

import (
	"context"
	"time"
)

type durableWork struct {
	work          Work
	queued, dirty bool // guarded by Runner.mu
}

// RegisterDurable registers a serialized scan of persistent work. Queue
// saturation can lose a wake, but never the underlying records: startup and
// periodic wakes retry them. The callback acknowledges only completed work.
func (r *Runner) RegisterDurable(kind string, work Work) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if r.queues[kind] == nil || r.durable[kind] != nil {
		r.mu.Unlock()
		panic("invalid durable job kind: " + kind)
	}
	r.durable[kind] = &durableWork{work: work}
	r.workers.Go(func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-ticker.C:
				r.WakeDurable(kind)
			}
		}
	})
	r.mu.Unlock()
	r.WakeDurable(kind)
}

func (r *Runner) WakeDurable(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	d := r.durable[kind]
	if d == nil {
		return
	}
	d.dirty = true
	if d.queued {
		return
	}
	select {
	case r.queues[kind] <- func(ctx context.Context) error {
		// Reset the scheduling flag even if the callback panics (run recovers).
		released := false
		defer func() {
			if !released {
				r.mu.Lock()
				d.queued = false
				r.mu.Unlock()
			}
		}()
		for {
			r.mu.Lock()
			d.dirty = false
			r.mu.Unlock()
			if err := d.work(ctx); err != nil {
				return err
			}
			r.mu.Lock()
			dirty := d.dirty
			if !dirty {
				d.queued = false
				released = true
			}
			r.mu.Unlock()
			if !dirty {
				return nil
			}
		}
	}:
		d.queued = true
		r.pending++
	default:
		// Persistent intent remains eligible for the next periodic wake.
	}
}
