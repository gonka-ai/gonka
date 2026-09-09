package main

import (
	"context"
	"io"
	"log/slog"
)

type prefixedTextHandler struct {
	prefix string
	inner  slog.Handler
}

func newPrefixedTextHandler(prefix string, w io.Writer, level slog.Level) slog.Handler {
	inner := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	if prefix == "" {
		return inner
	}
	return &prefixedTextHandler{prefix: prefix, inner: inner}
}

func (h *prefixedTextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *prefixedTextHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Message = "[" + h.prefix + "] " + r.Message
	return h.inner.Handle(ctx, r)
}

func (h *prefixedTextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &prefixedTextHandler{prefix: h.prefix, inner: h.inner.WithAttrs(attrs)}
}

func (h *prefixedTextHandler) WithGroup(name string) slog.Handler {
	return &prefixedTextHandler{prefix: h.prefix, inner: h.inner.WithGroup(name)}
}
