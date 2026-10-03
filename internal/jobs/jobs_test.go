package jobs

import (
	"context"
	"testing"
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
