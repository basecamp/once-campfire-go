package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type failingPrecomposedWriter struct{ *httptest.ResponseRecorder }

func (w *failingPrecomposedWriter) WritePrecomposed(int, []byte, [][]byte) error {
	return io.ErrClosedPipe
}

func TestPrecomposedFailureAbortsHandler(t *testing.T) {
	w := &failingPrecomposedWriter{httptest.NewRecorder()}
	buffer := &responseBuffer{
		ResponseWriter: w,
		precomposed:    true,
		recordedStatus: http.StatusOK,
		recordedHead:   []byte("Content-Length: 1\r\n"),
		parts:          [][]byte{[]byte("x")},
	}
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Errorf("panic = %v, want http.ErrAbortHandler", got)
		}
		if w.Body.Len() != 0 {
			t.Errorf("fallback emitted %q after failed frame", w.Body.String())
		}
	}()
	buffer.finish(httptest.NewRequest("GET", "/", nil))
}
