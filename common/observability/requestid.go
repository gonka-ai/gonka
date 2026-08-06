package observability

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

type requestIDKey struct{}

var requestSeq uint64

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
		id = strings.TrimSpace(ids[0])
	}
	if id == "" {
		seq := atomic.AddUint64(&requestSeq, 1)
		id = fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), seq)
	}
	return context.WithValue(ctx, requestIDKey{}, id), id
}

// SetRequestID forces id onto ctx, replacing any existing request ID.
// Empty/whitespace ids leave ctx unchanged.
func SetRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID stored on ctx, if any.
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
