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
	"unsafe"
)

type Writer struct {
	ctx    *C.ZSTD_CCtx
	output io.Writer
	buffer []byte
	err    error
}

func NewWriter(output io.Writer) (*Writer, error) {
	ctx := C.ZSTD_createCCtx()
	if ctx == nil {
		return nil, errors.New("zstd context allocation failed")
	}
	code := C.ZSTD_CCtx_setParameter(ctx, C.ZSTD_c_compressionLevel, 1)
	if C.ZSTD_isError(code) != 0 {
		C.ZSTD_freeCCtx(ctx)
		return nil, errors.New(C.GoString(C.ZSTD_getErrorName(code)))
	}
	return &Writer{ctx: ctx, output: output, buffer: make([]byte, int(C.ZSTD_CStreamOutSize()))}, nil
}
func (w *Writer) step(p []byte, mode C.int) (int, bool, error) {
	var consumed, written C.size_t
	code := C.step(w.ctx, unsafe.Pointer(unsafe.SliceData(w.buffer)), C.size_t(len(w.buffer)), unsafe.Pointer(unsafe.SliceData(p)), C.size_t(len(p)), &consumed, &written, mode)
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
func (w *Writer) Close() error {
	if w.ctx == nil {
		return w.err
	}
	defer func() { C.ZSTD_freeCCtx(w.ctx); w.ctx = nil }()
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
