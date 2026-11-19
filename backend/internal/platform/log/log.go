// Package log builds the JSON slog logger every Konduite process uses. It
// adds the request and correlation IDs carried in a context and redacts
// secrets by key and by shape before anything is written.
package log

import (
	"context"
	"io"
	"log/slog"
)

// New returns a JSON logger writing to w at the given level.
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr})
	return slog.New(&contextHandler{base: base, inner: base})
}

type ctxKey int

const (
	requestIDKey ctxKey = iota
	correlationIDKey
)

// WithRequestID returns ctx carrying id as the request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID returns the request ID in ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithCorrelationID returns ctx carrying id as the correlation ID.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

// CorrelationID returns the correlation ID in ctx, or "".
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey).(string)
	return id
}

// contextHandler keeps the IDs at the top level of the entry even after
// WithGroup: it records With* calls and replays them on top of the IDs,
// because attrs added to a record land inside the innermost open group.
type contextHandler struct {
	base  slog.Handler
	inner slog.Handler
	ops   []func(slog.Handler) slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	var ids []slog.Attr
	if id := RequestID(ctx); id != "" {
		ids = append(ids, slog.String("request_id", id))
	}
	if id := CorrelationID(ctx); id != "" {
		ids = append(ids, slog.String("correlation_id", id))
	}
	if len(ids) == 0 {
		return h.inner.Handle(ctx, r)
	}
	handler := h.base.WithAttrs(ids)
	for _, op := range h.ops {
		handler = op(handler)
	}
	return handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h.with(func(next slog.Handler) slog.Handler { return next.WithAttrs(attrs) })
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return h.with(func(next slog.Handler) slog.Handler { return next.WithGroup(name) })
}

func (h *contextHandler) with(op func(slog.Handler) slog.Handler) slog.Handler {
	ops := make([]func(slog.Handler) slog.Handler, len(h.ops), len(h.ops)+1)
	copy(ops, h.ops)
	return &contextHandler{base: h.base, inner: op(h.inner), ops: append(ops, op)}
}
