package main

import (
	"context"
	"log/slog"
	"os"

	commonobs "common/observability"
)

type prefixedTextHandler struct {
	prefix string
	inner  slog.Handler
}

// prefixTextHandler stamps [version] on text log lines so binaries running
// under versiond can be told apart. An empty prefix leaves the handler as-is.
func prefixTextHandler(prefix string, inner slog.Handler) slog.Handler {
	if prefix == "" || inner == nil {
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

func hostLoggerOptions(format, binaryVersion, levelRaw string) commonobs.LoggerOptions {
	return commonobs.LoggerOptions{
		Format: format,
		Level:  commonobs.ParseLogLevel(levelRaw),
		WrapText: func(inner slog.Handler) slog.Handler {
			return prefixTextHandler(binaryVersion, inner)
		},
		Attrs: []slog.Attr{slog.String("binary_version", binaryVersion)},
	}
}

// installHostLogger installs the process logger: text lines keep the
// [binary-version] prefix, JSON lines carry binary_version, and
// DEVSHARD_LOG_LEVEL selects the minimum level.
func installHostLogger(format, binaryVersion string) {
	commonobs.InstallLoggerWithOptions(hostLoggerOptions(format, binaryVersion, os.Getenv("DEVSHARD_LOG_LEVEL")))
}
