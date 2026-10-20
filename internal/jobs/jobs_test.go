package jobs

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestIndependentQueuesAndShutdown(t *testing.T) {
	r := New(1, "slow", "fast")
	entered := make(chan struct{})
	release := make(chan struct{})
	fast := make(chan struct{})
	r.Enqueue(testTask{queue: "slow", run: func(context.Context) error { close(entered); <-release; return nil }})
	<-entered
	r.Enqueue(testTask{queue: "fast", run: func(context.Context) error { close(fast); return nil }})
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatal("slow job blocked independent queue")
	}
	close(release)
	r.Close(time.Second)
	if admission := r.Enqueue(testTask{queue: "fast", run: func(context.Context) error { return nil }}); admission != Closed {
		t.Fatal("accepted work after shutdown")
	}
}

func TestConcurrentShutdown(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "drain"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := New(1, "work")
				release := make(chan struct{})
				r.Enqueue(testTask{queue: "work", run: func(ctx context.Context) error {
					select {
					case <-release:
					case <-ctx.Done():
					}
					return nil
				}})
				var closers sync.WaitGroup
				for range 2 {
					closers.Go(func() { r.Close(time.Minute) })
				}
				synctest.Wait() // Both closers and the active job are blocked.
				start := time.Now()
				if !timeout {
					close(release)
				}
				closers.Wait()
				synctest.Wait()
				want := time.Duration(0)
				if timeout {
					want = time.Minute
				}
				if elapsed := time.Since(start); elapsed != want {
					t.Fatalf("shutdown took %s; want %s", elapsed, want)
				}
				if admission := r.Enqueue(testTask{queue: "work", run: func(context.Context) error { return nil }}); admission != Closed {
					t.Fatal("accepted work after concurrent shutdown")
				}
			})
		})
	}
}

func TestShutdownJoinsCancelledWorkForEveryCloser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New(1, "work")
		cancelled := make(chan struct{})
		release := make(chan struct{})
		defer close(release)
		r.Enqueue(testTask{queue: "work", run: func(ctx context.Context) error {
			<-ctx.Done()
			close(cancelled)
			<-release // Cancellation cannot preempt every owned operation.
			return nil
		}})
		var closers sync.WaitGroup
		returned := make(chan struct{}, 2)
		for range 2 {
			closers.Go(func() { r.Close(time.Second); returned <- struct{}{} })
		}
		<-cancelled
		synctest.Wait()
		select {
		case <-returned:
			t.Fatal("shutdown returned while cancelled work still owned resources")
		default:
		}
		// Release without closing twice; the deferred close also handles failures.
		release <- struct{}{}
		closers.Wait()
	})
}

func TestShutdownDrainsDependentJobs(t *testing.T) {
	r := New(1, "parent", "child")
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	child := make(chan struct{})
	r.Enqueue(testTask{queue: "parent", run: func(context.Context) error {
		close(entered)
		<-release
		if admission := r.Enqueue(testTask{queue: "child", run: func(context.Context) error { close(child); return nil }}); admission != Accepted {
			t.Error("dependent job dropped")
		}
		return nil
	}})
	<-entered
	go func() { r.Close(time.Second); close(done) }()
	close(release)
	<-done
	select {
	case <-child:
	default:
		t.Fatal("dependent job did not finish")
	}
}

type testTask struct {
	queue string
	run   func(context.Context) error
}

func (task testTask) Queue() string                 { return task.queue }
func (task testTask) Run(ctx context.Context) error { return task.run(ctx) }

// Saturation must distinguish rejection from admission, and cancelled queued
// payloads must not remain retained after all workers have been joined.
func TestAdmissionAndCancelledQueueRetention(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New(1, "work")
		entered := make(chan struct{})
		if got := r.Enqueue(testTask{queue: "work", run: func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			return nil
		}}); got != Accepted {
			t.Fatal(got)
		}
		<-entered
		queued := testTask{queue: "work", run: func(context.Context) error { return nil }}
		for range 1024 {
			if got := r.Enqueue(queued); got != Accepted {
				t.Fatal(got)
			}
		}
		if got := r.Enqueue(queued); got != Full {
			t.Fatalf("saturation: %s", got)
		}
		r.Close(time.Second)
		if got := r.Enqueue(queued); got != Closed {
			t.Fatalf("shutdown: %s", got)
		}
		if len(r.queues) != 0 || r.pending != 0 {
			t.Fatal("cancelled queue still retains admitted tasks")
		}
	})
}
