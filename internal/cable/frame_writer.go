package cable

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// frameWriter owns one reusable write deadline. The connection context also
// interrupts lock waits and socket I/O through websocket's cancellation hook.
// Only the connection's writer goroutine may call Write or Close.
type frameWriter struct {
	ctx    context.Context
	cancel context.CancelFunc
	conn   *websocket.Conn
	limit  time.Duration
	timer  *time.Timer
}

func newFrameWriter(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, limit time.Duration) *frameWriter {
	w := &frameWriter{ctx: ctx, cancel: cancel, conn: conn, limit: limit}
	w.timer = time.AfterFunc(limit, cancel)
	if !w.timer.Stop() {
		cancel()
	}
	return w
}

func (w *frameWriter) Write(frame *websocket.PreparedMessage) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	w.timer.Reset(w.limit)
	err := w.conn.WritePrepared(w.ctx, frame)
	if !w.timer.Stop() {
		// An expired callback may still be waiting to run. Cancel synchronously
		// before returning so no subsequent write can rearm the expired deadline.
		w.cancel()
		return context.DeadlineExceeded
	}
	return err
}

func (w *frameWriter) Close() { w.timer.Stop() }
