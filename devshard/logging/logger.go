package logging

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// Logger is the interface for structured logging in the devshard package.
// Callers pass subsystem as a keyval: Info("applied diff", "subsystem", "state", "nonce", 5).
// When dapi integrates, it calls SetLogger() with an adapter that routes to
// dapi's configured slog handler.
type Logger interface {
	Info(msg string, keyvals ...any)
	Error(msg string, keyvals ...any)
	Warn(msg string, keyvals ...any)
	Debug(msg string, keyvals ...any)
}

var current Logger = &slogLogger{}

type requestIDKey struct{}

var requestSeq uint64

func SetLogger(l Logger) { current = l }

func Info(msg string, keyvals ...any)  { current.Info(msg, keyvals...) }
func Error(msg string, keyvals ...any) { current.Error(msg, keyvals...) }
func Warn(msg string, keyvals ...any)  { current.Warn(msg, keyvals...) }
func Debug(msg string, keyvals ...any) { current.Debug(msg, keyvals...) }

type slogLogger struct{}

func (s *slogLogger) Info(msg string, kv ...any)  { slog.Info(msg, kv...) }
func (s *slogLogger) Error(msg string, kv ...any) { slog.Error(msg, kv...) }
func (s *slogLogger) Warn(msg string, kv ...any)  { slog.Warn(msg, kv...) }
func (s *slogLogger) Debug(msg string, kv ...any) { slog.Debug(msg, kv...) }

// NewSlogAdapter returns a Logger that routes to the default slog handler and
// prefixes every record with the given keyvals. Intended for embedders (e.g.
// the dapi binary) that want devshard logs to land in their slog output with
// a fixed marker like "subsystem=devshardd".
func NewSlogAdapter(prefixKV ...any) Logger {
	dup := make([]any, len(prefixKV))
	copy(dup, prefixKV)
	return &prefixedSlogLogger{prefix: dup}
}

type prefixedSlogLogger struct {
	prefix []any
}

func (p *prefixedSlogLogger) merge(kv []any) []any {
	out := make([]any, 0, len(p.prefix)+len(kv))
	out = append(out, p.prefix...)
	out = append(out, kv...)
	return out
}

func (p *prefixedSlogLogger) Info(msg string, kv ...any)  { slog.Info(msg, p.merge(kv)...) }
func (p *prefixedSlogLogger) Error(msg string, kv ...any) { slog.Error(msg, p.merge(kv)...) }
func (p *prefixedSlogLogger) Warn(msg string, kv ...any)  { slog.Warn(msg, p.merge(kv)...) }
func (p *prefixedSlogLogger) Debug(msg string, kv ...any) { slog.Debug(msg, p.merge(kv)...) }

// WithRequestID attaches a request ID to the context. If one already exists
// it is preserved. Optional ids[0] supplies an explicit ID (e.g. validate-*).
// Returns the (possibly new) context and the request ID.
func WithRequestID(ctx context.Context, ids ...string) (context.Context, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id, ok := RequestID(ctx); ok {
		return ctx, id
	}
	id := ""
	if len(ids) > 0 {
		id = ids[0]
	}
	if id == "" {
		seq := atomic.AddUint64(&requestSeq, 1)
		id = fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), seq)
	}
	return context.WithValue(ctx, requestIDKey{}, id), id
}

func RequestID(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(requestIDKey{}).(string)
	return id, ok && id != ""
}

// PropagateRequestID copies the request ID from src into dst.
// Returns dst unchanged if src has no request ID.
func PropagateRequestID(dst, src context.Context) context.Context {
	if id, ok := RequestID(src); ok {
		return context.WithValue(dst, requestIDKey{}, id)
	}
	return dst
}

// Stage emits a structured log line in the canonical format:
//
//	request=req-... stage=some_stage key1=val1 key2=val2
//
// All layers (Proxy, Redundancy, Session) should use this so that logs
// are uniform and grepable by request ID.
func Stage(ctx context.Context, stage string, kv ...any) {
	fields := make([]string, 0, 2+len(kv)/2)
	if id, ok := RequestID(ctx); ok {
		fields = append(fields, "request="+id)
	}
	fields = append(fields, "stage="+stage)
	for i := 0; i < len(kv); i += 2 {
		key := fmt.Sprintf("field_%d", i)
		if s, ok := kv[i].(string); ok && s != "" {
			key = s
		}
		value := "<missing>"
		if i+1 < len(kv) {
			value = fmt.Sprint(kv[i+1])
		}
		fields = append(fields, key+"="+sanitize(value))
	}
	log.Print(strings.Join(fields, " "))
}

func sanitize(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, " \t\n\r\"") {
		return fmt.Sprintf("%q", v)
	}
	return v
}
