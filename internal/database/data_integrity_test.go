package database

import (
	"context"
	"sync"
	"testing"
	"time"
)

type commitBarrierLane struct {
	laneConn
	once              sync.Once
	committed, resume chan struct{}
}
type commitBarrierTx struct {
	laneTx
	owner *commitBarrierLane
}

func (l *commitBarrierLane) begin(ctx context.Context) (laneTx, error) {
	tx, err := l.laneConn.begin(ctx)
	if err != nil {
		return nil, err
	}
	return &commitBarrierTx{tx, l}, nil
}
func (tx *commitBarrierTx) commit() error {
	err := tx.laneTx.commit()
	if err == nil {
		tx.owner.once.Do(func() { close(tx.owner.committed); <-tx.owner.resume })
	}
	return err
}

func TestQueuedCreateIndexConcurrentMutation(t *testing.T) {
	for _, mode := range []string{"on", "off"} {
		for _, remove := range []bool{false, true} {
			name := mode + "/edit"
			if remove {
				name = mode + "/delete"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("CAMPFIRE_FASTDB_WRITE", mode)
				d := testDB(t)
				ctx := context.Background()
				u, err := d.Setup(ctx, "Index", "index@test", "digest")
				if err != nil {
					t.Fatal(err)
				}
				rooms, err := d.Rooms(ctx, u.ID)
				if err != nil {
					t.Fatal(err)
				}
				d.writer.Close()
				w, _ := batchWriter(d)
				barrier := &commitBarrierLane{laneConn: d.lane, committed: make(chan struct{}), resume: make(chan struct{})}
				w.conn = barrier
				w.busy = true
				d.writer = w
				finished := make(chan error, 1)
				go func() {
					_, err := d.CreateMessage(ctx, u.ID, rooms[0].ID, "", "<p>original</p>", "original")
					finished <- err
				}()
				job := <-w.jobs
				batchDone := make(chan struct{})
				go func() { w.runBatch(job); close(batchDone) }()
				<-barrier.committed
				var id int64
				if err := d.Read.QueryRowContext(ctx, "SELECT id FROM messages ORDER BY id DESC LIMIT 1").Scan(&id); err != nil {
					t.Fatal(err)
				}
				if remove {
					err = d.DeleteMessage(ctx, u.ID, id)
				} else {
					_, err = d.UpdateMessage(ctx, u.ID, id, "<p>replacement</p>", "replacement")
				}
				close(barrier.resume)
				<-batchDone
				// This manually driven writer has no loop to close.
				close(w.done)
				if err != nil {
					t.Fatal(err)
				}
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
				var count int
				if err := d.Read.QueryRowContext(ctx, "SELECT count(*) FROM message_search_index WHERE rowid=?", id).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if remove {
					if count != 0 {
						t.Fatalf("deleted message retains %d FTS rows", count)
					}
					return
				}
				var indexed string
				if err := d.Read.QueryRowContext(ctx, "SELECT body FROM message_search_index WHERE rowid=?", id).Scan(&indexed); err != nil {
					t.Fatal(err)
				}
				if indexed != "replacement" {
					t.Fatalf("FTS body = %q, want replacement", indexed)
				}
				hits, err := d.Search(ctx, u.ID, "replacement")
				if err != nil {
					t.Fatal(err)
				}
				if len(hits) != 1 {
					t.Fatalf("edited body missing from FTS: %d hits", len(hits))
				}
				old, err := d.Search(ctx, u.ID, "original")
				if err != nil {
					t.Fatal(err)
				}
				if len(old) != 0 {
					t.Fatalf("previous body remains indexed: %d hits", len(old))
				}
			})
		}
	}
}

func TestNextJobWaitsForInlineOwnership(t *testing.T) {
	w := &messageWriter{jobs: make(chan *messageJob, 3), wake: make(chan struct{}, 1), stop: make(chan struct{}), busy: true}
	a := &messageJob{}
	w.jobs <- a
	claimed := make(chan *messageJob, 1)
	go func() { job, _ := w.nextJob(); claimed <- job }()
	select {
	case <-claimed:
		t.Fatal("queued job claimed while inline owner active")
	case <-time.After(20 * time.Millisecond):
	}
	w.mu.Lock()
	w.busy = false
	w.mu.Unlock()
	w.wake <- struct{}{}
	select {
	case got := <-claimed:
		if got != a {
			t.Fatal("wrong job")
		}
	case <-time.After(time.Second):
		t.Fatal("release did not wake loop")
	}
}

// beginBarrierLane pauses after submit claims inline ownership but before the
// underlying connection can serialize transactions, exposing owner overlap.
type beginBarrierLane struct {
	laneConn
	once            sync.Once
	started, resume chan struct{}
}

func (l *beginBarrierLane) begin(ctx context.Context) (laneTx, error) {
	l.started <- struct{}{}
	l.once.Do(func() { <-l.resume })
	return l.laneConn.begin(ctx)
}

func TestInlineOwnershipThroughShutdown(t *testing.T) {
	d := testDB(t)
	d.writer.Close()
	w, _ := batchWriter(d)
	barrier := &beginBarrierLane{laneConn: d.lane, started: make(chan struct{}, 8), resume: make(chan struct{})}
	w.conn = barrier
	d.writer = w
	go w.loop()
	job := func(id int64) *messageJob {
		return &messageJob{ctx: context.Background(), run: func(jobTx) (Message, error) { return Message{ID: id}, nil }, done: make(chan messageResult, 1)}
	}
	a, b, c := job(1), job(2), job(3)
	aReturned := make(chan struct{})
	go func() { _ = w.submit(a); close(aReturned) }()
	<-barrier.started
	if err := w.submit(b); err != nil {
		t.Fatal(err)
	}
	if err := w.submit(c); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { w.Close(); close(closed) }()
	overlapped := false
	select {
	case <-barrier.started:
		overlapped = true
	case <-time.After(20 * time.Millisecond):
	}
	close(barrier.resume)
	<-closed
	<-aReturned
	if overlapped {
		t.Fatal("shutdown/queue entered begin while inline owner paused")
	}
	for _, j := range []*messageJob{a, b, c} {
		if result := <-j.done; result.err != nil {
			t.Fatal(result.err)
		}
	}
}
