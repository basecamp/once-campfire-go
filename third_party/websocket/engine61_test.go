//go:build !js

// ENGINE-61 regression: the hub's batched write arms the socket deadline at
// most once per cableDeadlineRearm and relies on it REMAINING armed between
// batches, so the deadline-less branch still runs under the remaining budget.
// WritePreparedBatchDeadlineRetained pins that contract: after a retained
// arm, a subsequent plain WritePreparedBatch on a socket whose peer stopped
// reading must fail at the original deadline without any re-arm or context.
// Under WritePreparedBatchDeadline's clear-after-write behaviour that write
// had no deadline at all and blocked until the test timeout.
package websocket

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestWritePreparedBatchDeadlineRetainedBoundsLaterWrite(t *testing.T) {
	t.Parallel()

	server, client := newPipeConnPair(t, noContextTakeover(), 256)
	msgs := []*PreparedMessage{
		NewPreparedMessage(MessageText, []byte("retained deadline")),
		NewPreparedMessage(MessageText, []byte(strings.Repeat("stall the unarmed write ", 32))),
	}

	// Round 1: the peer reads; the retained arm must not block the write.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		reader := bufio.NewReader(client.rwc.(net.Conn))
		for i := 0; i < len(msgs); i++ {
			h, err := readFrameHeader(reader, make([]byte, 8))
			if err != nil {
				t.Errorf("round 1 frame %d: %v", i, err)
				return
			}
			if _, err := io.CopyN(io.Discard, reader, h.payloadLength); err != nil {
				t.Errorf("round 1 frame %d payload: %v", i, err)
				return
			}
		}
	}()
	if err := server.WritePreparedBatchDeadlineRetained(time.Now().Add(time.Second), msgs); err != nil {
		t.Fatalf("round 1 (retained arm): %v", err)
	}
	<-readerDone

	// Round 2: the peer stops reading. A deadline-less write (the hub's
	// unarmed ENGINE-61 branch) must fail when the retained deadline passes,
	// with no re-arm and no context: if the arm had been cleared after round
	// 1, this call would block until the test timeout.
	start := time.Now()
	err := server.WritePreparedBatch(context.Background(), msgs)
	if err == nil {
		t.Fatal("stalled write under a retained deadline returned nil")
	}
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Fatalf("stalled write took %v to fail: retained deadline did not stay armed", elapsed)
	}
	// The failure must come from the ORIGINAL retained deadline (~1s after
	// the arm), not from an immediate error.
	if elapsed < 500*time.Millisecond {
		t.Fatalf("stalled write failed after %v, before the retained deadline could fire", elapsed)
	}
}
