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
