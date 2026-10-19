//go:build !js

// ENGINE-40 micro-benchmarks: batched vs single-frame writes and the
// exact-read vs streaming-read paths. Values are relative, not absolute;
// run on a quiet machine with -benchmem.
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

func benchConnPair(b *testing.B) (server, client *Conn) {
	b.Helper()
	clientSide, serverSide := net.Pipe()
	server = newConn(connConfig{
		rwc:            serverSide,
		client:         false,
		copts:          CompressionNoContextTakeover.opts(),
		flateThreshold: 512,
		readSrc:        serverSide,
		bw:             bufio.NewWriterSize(serverSide, 4096),
	})
	client = newConn(connConfig{
		rwc:            clientSide,
		client:         true,
		copts:          CompressionNoContextTakeover.opts(),
		flateThreshold: 512,
		br:             bufio.NewReader(clientSide),
		bw:             bufio.NewWriterSize(clientSide, 4096),
	})
	b.Cleanup(func() { server.CloseNow(); client.CloseNow() })
	return server, client
}

const benchBatch = 8

func benchmarkPrepared(b *testing.B, batch bool) {
	ctx := context.Background()
	msgs := make([]*PreparedMessage, benchBatch)
	for i := range msgs {
		msgs[i] = NewPreparedMessage(MessageText, []byte(strings.Repeat("broadcast frame ", 8)))
	}
	server, client := benchConnPair(b)
	done := make(chan error, 1)
	go func() {
		// Consume the frames as they arrive.
		br := bufio.NewReader(client.rwc.(net.Conn))
		var err error
		for i := 0; i < b.N*benchBatch; i++ {
			h, e := readFrameHeader(br, make([]byte, 8))
			if e != nil {
				err = e
				break
			}
			if _, e := io.CopyN(io.Discard, br, h.payloadLength); e != nil {
				err = e
				break
			}
		}
		done <- err
	}()
	b.ReportAllocs()
	b.SetBytes(int64(len(msgs[0].data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if batch {
			if err := server.WritePreparedBatch(ctx, msgs); err != nil {
				b.Fatal(err)
			}
		} else {
			for _, m := range msgs {
				if err := server.WritePrepared(ctx, m); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	b.StopTimer()
	if err := <-done; err != nil {
		b.Fatal(err)
	}
}

func BenchmarkWritePreparedSingle(b *testing.B) {
	benchmarkPrepared(b, false)
}

func BenchmarkWritePreparedBatch(b *testing.B) {
	benchmarkPrepared(b, true)
}

// ENGINE-54: the hub's per-wake write bound, old (a fresh 30-second context
// per write, as the writer loop did through ENGINE-40b) vs new (one absolute
// socket deadline set before the vectored write and cleared after). Real TCP
// loopback so the batch takes the writev path; the sink eats frames as fast
// as they arrive.

func benchTCPConnPair(b *testing.B) (server, client *Conn) {
	b.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	closed := make(chan struct{})
	acceptDone := make(chan *Conn, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			close(closed)
			acceptDone <- nil
			return
		}
		acceptDone <- newConn(connConfig{
			rwc:            conn,
			client:         false,
			copts:          CompressionNoContextTakeover.opts(),
			flateThreshold: 512,
			readSrc:        conn,
			bw:             bufio.NewWriterSize(conn, 4096),
		})
		<-closed
	}()
	clientSide, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	client = newConn(connConfig{
		rwc:            clientSide,
		client:         true,
		copts:          CompressionNoContextTakeover.opts(),
		flateThreshold: 512,
		br:             bufio.NewReader(clientSide),
		bw:             bufio.NewWriterSize(clientSide, 4096),
	})
	server = <-acceptDone
	if server == nil {
		b.Fatal("accept failed")
	}
	b.Cleanup(func() { close(closed); l.Close(); server.CloseNow(); client.CloseNow() })
	return server, client
}

// benchmarkWakeWrite measures one coalesced write of len(msgs) frames as the
// hub writer performs it per wake. oldCtx arms a fresh 30-second context per
// write (the pre-ENGINE-54 pattern); deadline uses WritePreparedBatchDeadline.
func benchmarkWakeWrite(b *testing.B, oldCtx bool) {
	msgs := make([]*PreparedMessage, benchBatch)
	for i := range msgs {
		msgs[i] = NewPreparedMessage(MessageText, []byte(strings.Repeat("broadcast frame ", 8)))
	}
	server, client := benchTCPConnPair(b)
	done := make(chan error, 1)
	go func() {
		br := bufio.NewReader(client.rwc.(net.Conn))
		var err error
		for i := 0; i < b.N*benchBatch; i++ {
			h, e := readFrameHeader(br, make([]byte, 8))
			if e != nil {
				err = e
				break
			}
			if _, e := io.CopyN(io.Discard, br, h.payloadLength); e != nil {
				err = e
				break
			}
		}
		done <- err
	}()
	b.ReportAllocs()
	b.SetBytes(int64(len(msgs[0].data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if oldCtx {
			timeout, stop := context.WithTimeout(context.Background(), 30*time.Second)
			if err := server.WritePreparedBatch(timeout, msgs); err != nil {
				b.Fatal(err)
			}
			stop()
		} else {
			if err := server.WritePreparedBatchDeadline(time.Now().Add(30*time.Second), msgs); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.StopTimer()
	if err := <-done; err != nil {
		b.Fatal(err)
	}
}

func BenchmarkWakeWriteWithTimeoutCtx(b *testing.B) {
	benchmarkWakeWrite(b, true)
}

func BenchmarkWakeWriteDeadline(b *testing.B) {
	benchmarkWakeWrite(b, false)
}

func benchmarkRead(b *testing.B, exact bool) {
	ctx := context.Background()
	payload := []byte(strings.Repeat("command payload ", 8))
	server, client := benchConnPair(b)
	sink := make(chan error, 1)
	go func() {
		var err error
		for i := 0; i < b.N; i++ {
			if _, _, err = server.Read(ctx); err != nil {
				break
			}
		}
		sink <- err
	}()
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.Write(ctx, MessageText, payload); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if err := <-sink; err != nil {
		b.Fatal(err)
	}
}

func BenchmarkReadExactServer(b *testing.B) {
	benchmarkRead(b, true)
}

func BenchmarkReadLegacyReader(b *testing.B) {
	// Legacy shape: Reader + io.ReadAll, as upstream Read did.
	ctx := context.Background()
	payload := []byte(strings.Repeat("command payload ", 8))
	server, client := benchConnPair(b)
	legacy := func() error {
		typ, r, err := server.Reader(ctx)
		if err != nil {
			return err
		}
		_, err = io.ReadAll(r)
		_ = typ
		return err
	}
	sink := make(chan error, 1)
	go func() {
		var err error
		for i := 0; i < b.N; i++ {
			if err = legacy(); err != nil {
				break
			}
		}
		sink <- err
	}()
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.Write(ctx, MessageText, payload); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if err := <-sink; err != nil {
		b.Fatal(err)
	}
}
