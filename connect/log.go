package connect

import (
	"context"
	"io"
	"os"
)

type logWriterKey struct{}

// WithLogWriter returns a new context carrying the given io.Writer for
// service logging. The rig client SDK sets this automatically for Func
// services so that log output is captured in the event timeline.
func WithLogWriter(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, logWriterKey{}, w)
}

// LogWriter returns an io.Writer for service log output. When running as
// a rig Func service, writes are shipped to the rigd event timeline. When
// running outside of rig (production, local dev), returns os.Stdout.
//
// The returned writer works directly with Go's standard logging:
//
//	slog.New(slog.NewTextHandler(connect.LogWriter(ctx), nil))
//	log.New(connect.LogWriter(ctx), "", 0)
//	log.SetOutput(connect.LogWriter(ctx))
func LogWriter(ctx context.Context) io.Writer {
	if w, ok := ctx.Value(logWriterKey{}).(io.Writer); ok && w != nil {
		return w
	}
	return os.Stdout
}
