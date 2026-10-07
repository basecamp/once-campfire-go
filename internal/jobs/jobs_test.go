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
	r.Enqueue("slow", func(context.Context) error { close(entered); <-release; return nil })
	<-entered
	r.Enqueue("fast", func(context.Context) error { close(fast); return nil })
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatal("slow job blocked independent queue")
	}
	close(release)
	r.Close(time.Second)
	if r.Enqueue("fast", func(context.Context) error { return nil }) {
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
				r.Enqueue("work", func(ctx context.Context) error {
					select {
					case <-release:
					case <-ctx.Done():
					}
					return nil
				})
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
				if r.Enqueue("work", func(context.Context) error { return nil }) {
					t.Fatal("accepted work after concurrent shutdown")
				}
			})
		})
	}
}

func TestShutdownDrainsDependentJobs(t *testing.T) {
	r := New(1, "parent", "child")
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	child := make(chan struct{})
	r.Enqueue("parent", func(context.Context) error {
		close(entered)
		<-release
		if !r.Enqueue("child", func(context.Context) error { close(child); return nil }) {
			t.Error("dependent job dropped")
		}
		return nil
	})
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
