package logging

import (
	"context"
	"log/slog"
)

// ErrorSink receives every Error-level record that was not already reported.
// Implementations must not block and must not log through the tapped logger.
type ErrorSink interface {
	LogError(ctx context.Context, r slog.Record)
}

// WithErrorSink returns a logger that writes exactly what base writes and
// additionally hands Error-level records to sink. The wrapped handler is
// base's own, so the output mutex is shared and lines never interleave.
func WithErrorSink(base *slog.Logger, sink ErrorSink) *slog.Logger {
	return slog.New(&sinkHandler{inner: base.Handler(), sink: sink})
}

// sinkHandler forwards to inner, then taps Error records to sink. It mirrors
// the attrs and group path added through WithAttrs/WithGroup so the forwarded
// record is self-contained.
type sinkHandler struct {
	inner    slog.Handler
	sink     ErrorSink
	attrs    []slog.Attr // already qualified with the group path
	groupPfx string      // dotted path of open groups, e.g. "req.db."
}

func (h *sinkHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *sinkHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.inner.Handle(ctx, r)

	if r.Level >= slog.LevelError && !IsReported(ctx) {
		out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
		out.AddAttrs(h.attrs...)
		r.Attrs(func(a slog.Attr) bool {
			a.Key = h.groupPfx + a.Key
			out.AddAttrs(a)
			return true
		})
		h.sink.LogError(ctx, out)
	}

	return err //nolint:wrapcheck // transparent tap: the inner handler's error passes through unchanged
}

func (h *sinkHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	for _, a := range attrs {
		a.Key = h.groupPfx + a.Key
		merged = append(merged, a)
	}

	return &sinkHandler{
		inner:    h.inner.WithAttrs(attrs),
		sink:     h.sink,
		attrs:    merged,
		groupPfx: h.groupPfx,
	}
}

func (h *sinkHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	return &sinkHandler{
		inner:    h.inner.WithGroup(name),
		sink:     h.sink,
		attrs:    h.attrs,
		groupPfx: h.groupPfx + name + ".",
	}
}
