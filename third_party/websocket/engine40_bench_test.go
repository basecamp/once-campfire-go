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
