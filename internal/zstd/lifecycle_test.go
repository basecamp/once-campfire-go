package zstd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
)

func contextReleased(owner *contextOwner) bool {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.ctx == nil
}

func awaitContextRelease(t *testing.T, owner *contextOwner) {
	t.Helper()
	t.Cleanup(owner.free)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if contextReleased(owner) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("unreachable writer retained its native zstd context")
}

//go:noinline
func abandonUnfinishedWriter(t *testing.T, output io.Writer) *contextOwner {
	t.Helper()
	writer, err := NewWriter(output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("unfinished frame")); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	return writer.owner
}

func TestAbandonedWriterReclaimsNativeContextWithoutFlushing(t *testing.T) {
	var output bytes.Buffer
	owner := abandonUnfinishedWriter(t, &output)
	before := bytes.Clone(output.Bytes())
	awaitContextRelease(t, owner)
	if !bytes.Equal(output.Bytes(), before) {
		t.Fatal("native cleanup wrote to the abandoned output")
	}
}

type cyclicWriterOutput struct {
	writer *Writer
	buffer bytes.Buffer
}

func (o *cyclicWriterOutput) Write(p []byte) (int, error) { return o.buffer.Write(p) }

//go:noinline
func discardCyclicPoolEntry(t *testing.T) *contextOwner {
	t.Helper()
	entry := &cyclicWriterOutput{}
	writer, err := NewWriter(entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.writer = writer
	if _, err := writer.Write([]byte("pooled frame")); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.End(); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	pool := new(sync.Pool)
	pool.Put(entry)
	return writer.owner
}

func TestDiscardedCyclicPoolEntryReclaimsNativeContext(t *testing.T) {
	awaitContextRelease(t, discardCyclicPoolEntry(t))
}

func TestReachableWriterKeepsNativeContextAcrossEndReset(t *testing.T) {
	var output bytes.Buffer
	writer, err := NewWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for _, payload := range []string{"first frame must not leak into reuse", "second independent frame"} {
		output.Reset()
		if err := writer.Reset(&output); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		if err := writer.End(); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		runtime.GC()
		if contextReleased(writer.owner) {
			t.Fatal("reachable reusable writer lost its native context")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := exec.CommandContext(ctx, "zstd", "-d", "-q", "-c")
		command.Stdin = bytes.NewReader(output.Bytes())
		decoded, err := command.Output()
		cancel()
		if err != nil || string(decoded) != payload {
			t.Fatalf("independent frame: %q, %v", decoded, err)
		}
	}
}

func TestCloseReleasesNativeContextIdempotently(t *testing.T) {
	writer, err := NewWriter(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	owner := writer.owner
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if !contextReleased(owner) {
		t.Fatal("Close retained the native context")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var done sync.WaitGroup
	for range 8 {
		done.Go(owner.free)
	}
	done.Wait()
	if err := writer.Reset(io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("reset after Close: %v", err)
	}
}

func TestOutputErrorStillReleasesNativeContext(t *testing.T) {
	writer, err := NewWriter(failingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	owner := writer.owner
	if _, err := writer.Write([]byte("failing output")); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("close output failure: %v", err)
	}
	if !contextReleased(owner) {
		t.Fatal("output failure retained the native context")
	}
}
