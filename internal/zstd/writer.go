// Package zstd exposes libzstd's bounded streaming encoder.
package zstd

/*
#cgo pkg-config: libzstd
#include <zstd.h>
static size_t step(ZSTD_CCtx *ctx, void *dst, size_t cap, const void *src, size_t len,
                   size_t *read, size_t *written, int finish) {
 ZSTD_inBuffer in = {src, len, 0};
 ZSTD_outBuffer out = {dst, cap, 0};
 size_t result = ZSTD_compressStream2(ctx, &out, &in, (ZSTD_EndDirective)finish);
 *read = in.pos;
 *written = out.pos;
 return result;
}
*/
import "C"

import (
	"errors"
	"io"
	"runtime"
	"sync"
	"unsafe"
)

// contextOwner has no reference to Writer or its output. Keeping native
// ownership separate permits reclamation even when a pooled writer and its
// output buffer form a Go reference cycle.
type contextOwner struct {
	mu  sync.Mutex
	ctx *C.ZSTD_CCtx
}

func (o *contextOwner) free() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ctx != nil {
		C.ZSTD_freeCCtx(o.ctx)
		o.ctx = nil
	}
}

// Writer owns one streaming encoder. It must not be copied or used concurrently.
// Close finishes the frame and releases native memory deterministically.
// Unreachable writers have a GC cleanup fallback that only frees native memory;
// it does not finish an abandoned frame and is not a prompt-release guarantee.
type Writer struct {
	ctx     *C.ZSTD_CCtx
	owner   *contextOwner
	cleanup runtime.Cleanup
	output  io.Writer
	buffer  []byte
	err     error
}

func NewWriter(output io.Writer) (*Writer, error) {
	return NewWriterLevel(output, 1)
}

// NewWriterLevel builds a writer that emits complete zstd frames at the given
// compression level. Levels follow libzstd's range; the default NewWriter
// maps to level 1. Callers that fill a one-time cache (the web recorded-piece
// path) pass the best level, whose cost is paid once per stored piece.
func NewWriterLevel(output io.Writer, level int) (*Writer, error) {
	ctx := C.ZSTD_createCCtx()
	if ctx == nil {
		return nil, errors.New("zstd context allocation failed")
	}
	code := C.ZSTD_CCtx_setParameter(ctx, C.ZSTD_c_compressionLevel, C.int(level))
	if C.ZSTD_isError(code) != 0 {
		C.ZSTD_freeCCtx(ctx)
		return nil, errors.New(C.GoString(C.ZSTD_getErrorName(code)))
	}
	writer := &Writer{ctx: ctx, owner: &contextOwner{ctx: ctx}, output: output, buffer: make([]byte, int(C.ZSTD_CStreamOutSize()))}
	// The cleanup owns only native state, not the writer or its output. It
	// therefore remains collectible when a discarded pool entry forms a cycle.
	writer.cleanup = runtime.AddCleanup(writer, (*contextOwner).free, writer.owner)
	return writer, nil
}
func (w *Writer) step(p []byte, mode C.int) (int, bool, error) {
	var consumed, written C.size_t
	code := C.step(w.ctx, unsafe.Pointer(unsafe.SliceData(w.buffer)), C.size_t(len(w.buffer)), unsafe.Pointer(unsafe.SliceData(p)), C.size_t(len(p)), &consumed, &written, mode)
	runtime.KeepAlive(w)
	if C.ZSTD_isError(code) != 0 {
		return int(consumed), false, errors.New(C.GoString(C.ZSTD_getErrorName(code)))
	}
	if written > 0 {
		n, err := w.output.Write(w.buffer[:int(written)])
		if err != nil {
			return int(consumed), false, err
		}
		if n != int(written) {
			return int(consumed), false, io.ErrShortWrite
		}
	}
	return int(consumed), code == 0, nil
}

// Reset reuses the context and buffer for a new frame written to output. The
// session state is reset and the error slot cleared; the compression level set
// at construction is retained. The writer must not be in use when Reset is
// called, and must not be used after Close.
func (w *Writer) Reset(output io.Writer) error {
	if w.ctx == nil {
		return io.ErrClosedPipe
	}
	code := C.ZSTD_CCtx_reset(w.ctx, C.ZSTD_reset_session_only)
	runtime.KeepAlive(w)
	if C.ZSTD_isError(code) != 0 {
		return errors.New(C.GoString(C.ZSTD_getErrorName(code)))
	}
	w.output = output
	w.err = nil
	return nil
}
func (w *Writer) Write(p []byte) (int, error) {
	if w.ctx == nil {
		return 0, io.ErrClosedPipe
	}
	if w.err != nil {
		return 0, w.err
	}
	consumed := 0
	for consumed < len(p) {
		n, _, err := w.step(p[consumed:], 0)
		consumed += n
		if err != nil {
			w.err = err
			return consumed, err
		}
	}
	return consumed, nil
}

// End finishes the current frame without releasing the context, so a pooled
// writer can be Reset for another frame afterwards. Close also finishes and
// then frees the context; the writer is unusable after Close.
func (w *Writer) End() error {
	if w.ctx == nil {
		return w.err
	}
	if w.err != nil {
		return w.err
	}
	for {
		_, done, err := w.step(nil, 2)
		if err != nil {
			w.err = err
			return err
		}
		if done {
			return nil
		}
	}
}
func (w *Writer) Close() error {
	if w.ctx == nil {
		return w.err
	}
	defer func() {
		w.cleanup.Stop()
		w.owner.free()
		w.ctx = nil
		runtime.KeepAlive(w)
	}()
	return w.End()
}

func (w *Writer) Flush() error {
	if w.ctx == nil {
		return io.ErrClosedPipe
	}
	if w.err != nil {
		return w.err
	}
	for {
		_, done, err := w.step(nil, 1)
		if err != nil {
			w.err = err
			return err
		}
		if done {
			return nil
		}
	}
}
