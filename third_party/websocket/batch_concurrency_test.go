//go:build !js

package websocket

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type batchGateWriter struct {
	output  bytes.Buffer
	entered chan struct{}
	release chan struct{}
	first   sync.Once
	unblock sync.Once
}

func (w *batchGateWriter) Read([]byte) (int, error) { return 0, io.EOF }
func (w *batchGateWriter) Write(p []byte) (int, error) {
	w.first.Do(func() {
		close(w.entered)
		<-w.release
	})
	return w.output.Write(p)
}
func (w *batchGateWriter) Close() error {
	w.unblock.Do(func() { close(w.release) })
	return nil
}

func TestPreparedBatchCanceledContenderPreservesActiveWrite(t *testing.T) {
	for _, mode := range []string{"context", "deadline", "retained"} {
		t.Run(mode, func(t *testing.T) {
			sink := &batchGateWriter{entered: make(chan struct{}), release: make(chan struct{})}
			server := newConn(connConfig{rwc: sink, readSrc: sink, bw: bufio.NewWriter(sink)})
			finished := make(chan struct{})
			result := make(chan error, 1)
			t.Cleanup(func() {
				sink.Close()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Error("active batch writer did not stop")
				}
				server.CloseNow()
			})
			payload := []byte("active")
			messages := []*PreparedMessage{NewPreparedMessage(MessageText, payload)}
			go func() {
				defer close(finished)
				switch mode {
				case "context":
					result <- server.WritePreparedBatch(context.Background(), messages)
				case "deadline":
					result <- server.WritePreparedBatchDeadline(time.Now().Add(time.Second), messages)
				case "retained":
					result <- server.WritePreparedBatchDeadlineRetained(time.Now().Add(time.Second), messages)
				}
			}()
			select {
			case <-sink.entered:
			case <-time.After(time.Second):
				t.Fatal("active writer did not reach the transport")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := server.WritePreparedBatch(ctx, []*PreparedMessage{
				NewPreparedMessage(MessageText, []byte("rejected one")),
				NewPreparedMessage(MessageBinary, []byte("rejected two")),
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled contender: %v", err)
			}
			sink.Close()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("active writer did not complete after release")
			}
			want := append([]byte{0x81, byte(len(payload))}, payload...)
			if got := sink.output.Bytes(); !bytes.Equal(got, want) {
				t.Fatalf("cancelled contender corrupted active frame: got %x, want %x", got, want)
			}
		})
	}
}

func TestPreparedBatchConcurrentWrites(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		name := "plain"
		if compressed {
			name = "compressed"
		}
		t.Run(name, func(t *testing.T) {
			var options *compressionOptions
			if compressed {
				options = noContextTakeover()
			}
			server, client := newPipeConnPair(t, options, 32)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			const writers, rounds = 4, 8
			type frameKey struct {
				kind MessageType
				data string
			}
			pending := make(map[frameKey]bool)
			var batches [writers][rounds][]*PreparedMessage
			for writer := range writers {
				for round := range rounds {
					for frame := 0; frame <= round%4; frame++ {
						payload := fmt.Sprintf("%d/%d/%d", writer, round, frame)
						if frame > 0 {
							payload += strings.Repeat(" compressible", frame*16)
						}
						kind := MessageText
						if frame%2 != 0 {
							kind = MessageBinary
						}
						batches[writer][round] = append(batches[writer][round], NewPreparedMessage(kind, []byte(payload)))
						pending[frameKey{kind, payload}] = true
					}
				}
			}
			readDone := make(chan error, 1)
			go func() {
				for len(pending) > 0 {
					kind, data, err := client.Read(ctx)
					if err != nil {
						client.CloseNow()
						readDone <- err
						return
					}
					key := frameKey{kind, string(data)}
					if !pending[key] {
						client.CloseNow()
						readDone <- fmt.Errorf("corrupt, duplicate or unexpected frame: type %d, %q", kind, data)
						return
					}
					delete(pending, key)
				}
				readDone <- nil
			}()
			start := make(chan struct{})
			writeDone := make(chan error, writers)
			for writer := range writers {
				go func() {
					<-start
					for _, batch := range batches[writer] {
						var err error
						switch writer {
						case 0:
							err = server.WritePreparedBatch(ctx, batch)
						case 1:
							err = server.WritePreparedBatchDeadline(time.Now().Add(5*time.Second), batch)
						case 2:
							err = server.WritePreparedBatchDeadlineRetained(time.Now().Add(5*time.Second), batch)
						case 3:
							for _, message := range batch {
								if err = server.WritePrepared(ctx, message); err != nil {
									break
								}
							}
						}
						if err != nil {
							writeDone <- err
							return
						}
					}
					writeDone <- nil
				}()
			}
			close(start)
			for range writers {
				select {
				case err := <-writeDone:
					if err != nil {
						t.Errorf("concurrent writer: %v", err)
					}
				case <-ctx.Done():
					server.CloseNow()
					client.CloseNow()
					t.Fatal("concurrent writers did not complete")
				}
			}
			select {
			case err := <-readDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				client.CloseNow()
				t.Fatal("concurrent frame audit did not complete")
			}
		})
	}
}
