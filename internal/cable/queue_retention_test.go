package cable

import (
	"errors"
	"github.com/coder/websocket"
	"testing"
)

func TestDrainBatchReleasesBackingPointers(t *testing.T) {
	for _, fail := range []bool{false, true} {
		q := newOutQueueCap(4)
		q.head, q.len = 3, 3 // wrap around the ring
		for _, i := range []int{3, 0, 1} {
			q.frames[i] = websocket.NewPreparedMessage(websocket.MessageText, []byte("payload"))
		}
		var batch [4]*websocket.PreparedMessage
		writeErr := errors.New("write failed")
		q.mu.Lock()
		err := q.drainBatch(batch[:], 4, func(n int) error {
			if n != 3 {
				t.Fatalf("batch length=%d", n)
			}
			for i, p := range q.frames {
				if p != nil {
					t.Errorf("consumed ring slot %d retained frame", i)
				}
			}
			for i := 0; i < n; i++ {
				if batch[i] == nil {
					t.Errorf("active batch slot %d missing frame", i)
				}
			}
			if fail {
				return writeErr
			}
			return nil
		})
		if fail != errors.Is(err, writeErr) {
			t.Fatalf("write error=%v", err)
		}
		for i, p := range batch {
			if p != nil {
				t.Errorf("completed batch slot %d retained frame", i)
			}
		}
		if q.len != 0 {
			t.Fatalf("queue length=%d", q.len)
		}
	}
}
