package zstd

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"testing"
)

func TestFlushAndClose(t *testing.T) {
	var compressed bytes.Buffer
	writer, err := NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte("first"))
	if err = writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() == 0 {
		t.Fatal("flush produced no data")
	}
	writer.Write([]byte("second"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("zstd", "-d", "-q", "-c")
	command.Stdin = &compressed
	decoded, err := command.Output()
	if err != nil || string(decoded) != "firstsecond" {
		t.Fatalf("%q %v", decoded, err)
	}
	if _, err = writer.Write([]byte("closed")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestOutputError(t *testing.T) {
	writer, err := NewWriter(failingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte("data"))
	if err = writer.Close(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

// TestNewWriterLevel pins the level parameter: a higher level must not
// inflate the frame, and every level must decode back to the input. The
// corpus is compressible text, so level 19 is expected to shrink level 1.
func TestNewWriterLevel(t *testing.T) {
	var corpus bytes.Buffer
	word := []byte("campfire message thread weekly update review deploy server cache render ")
	for corpus.Len() < 64<<10 {
		corpus.Write(word)
	}
	sizes := make([]int, 0, 22)
	for level := 1; level <= 19; level++ {
		var compressed bytes.Buffer
		writer, err := NewWriterLevel(&compressed, level)
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		if _, err := writer.Write(corpus.Bytes()); err != nil {
			t.Fatalf("level %d write: %v", level, err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("level %d close: %v", level, err)
		}
		sizes = append(sizes, compressed.Len())
		if compressed.Len() >= corpus.Len() {
			t.Fatalf("level %d produced %d bytes, not smaller than %d input", level, compressed.Len(), corpus.Len())
		}
		command := exec.Command("zstd", "-d", "-q", "-c")
		command.Stdin = &compressed
		decoded, err := command.Output()
		if err != nil || !bytes.Equal(decoded, corpus.Bytes()) {
			t.Fatalf("level %d round trip: %v", level, err)
		}
	}
	if sizes[0] <= sizes[len(sizes)-1] {
		t.Fatalf("level 19 (%d bytes) not smaller than level 1 (%d bytes)", sizes[len(sizes)-1], sizes[0])
	}
}
