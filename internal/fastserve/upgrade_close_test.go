package fastserve

import (
	"errors"
	"net"
	"sync"
	"testing"
)

func TestQueueListenerConcurrentClose(t *testing.T) {
	const closers = 64
	for attempt := 0; attempt < 128; attempt++ {
		listener := newQueueListener(handoffAddr{})
		start := make(chan struct{})
		panics := make(chan any, closers)
		var done sync.WaitGroup
		done.Add(closers)
		for range closers {
			go func() {
				defer done.Done()
				defer func() {
					if value := recover(); value != nil {
						panics <- value
					}
				}()
				<-start
				if err := listener.Close(); err != nil {
					t.Errorf("close: %v", err)
				}
			}()
		}
		close(start)
		done.Wait()
		if len(panics) != 0 {
			t.Fatalf("concurrent Close panicked in trial %d: %v", attempt+1, <-panics)
		}
		if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("accept after close: %v", err)
		}
		if listener.push(nil) {
			t.Fatal("closed listener accepted a handoff")
		}
		if err := listener.Close(); err != nil {
			t.Fatalf("repeated close: %v", err)
		}
	}
}
