//go:build !js

package websocket

import (
	"context"
	"testing"
	"time"
)

func TestPreparedBatchReleasesScratch(t *testing.T) {
	server, client := newPipeConnPair(t, nil, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msgs := make([]*PreparedMessage, 32)
	for i := range msgs {
		msgs[i] = NewPreparedMessage(MessageText, []byte("retention payload"))
	}
	readDone := make(chan error, 1)
	go func() {
		for i := 0; i < len(msgs)+1; i++ {
			if _, _, err := client.Read(ctx); err != nil {
				readDone <- err
				return
			}
		}
		readDone <- nil
	}()
	check := func() {
		t.Helper()
		for i, data := range server.batchData[:cap(server.batchData)] {
			if data != nil {
				t.Errorf("batchData slot %d retains payload", i)
				break
			}
		}
		for i, data := range server.batchBufs[:cap(server.batchBufs)] {
			if data != nil {
				t.Errorf("batchBufs slot %d retains payload", i)
				break
			}
		}
	}
	if err := server.WritePreparedBatch(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	check()
	dataCap, bufCap := cap(server.batchData), cap(server.batchBufs)
	if err := server.WritePreparedBatch(ctx, msgs[:1]); err != nil {
		t.Fatal(err)
	}
	check()
	if cap(server.batchData) != dataCap || cap(server.batchBufs) != bufCap {
		t.Fatal("scratch backing arrays were discarded")
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	server.CloseNow()
	if err := server.WritePreparedBatch(ctx, msgs[:1]); err == nil {
		t.Fatal("closed connection write succeeded")
	}
	check()
}
