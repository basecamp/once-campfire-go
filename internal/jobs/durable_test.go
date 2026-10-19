package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestDurableWakeSurvivesQueueSaturation(t *testing.T) {
	r := New(1, "push")
	gate := make(chan struct{})
	started := make(chan struct{})
	r.Enqueue("push", func(context.Context) error { close(started); <-gate; return nil })
	<-started
	for i := 0; i < 1024; i++ {
		if !r.Enqueue("push", func(context.Context) error { return nil }) {
			t.Fatal("queue filled early")
		}
	}
	var pending atomic.Bool
	pending.Store(true)
	delivered := make(chan struct{}, 1)
	// A durable work callback scans persisted intent, rather than relying on
	// retention of an individual in-memory enqueue closure.
	durable, ok := any(r).(interface{ RegisterDurable(string, Work) })
	if !ok {
		close(gate)
		r.Close(time.Second)
		t.Fatal("runner has no retryable durable wake")
	}
	durable.RegisterDurable("push", func(context.Context) error {
		if pending.Swap(false) {
			delivered <- struct{}{}
		}
		return nil
	})
	close(gate)
	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("saturated durable wake lost")
	}
	r.Close(time.Second)
}
