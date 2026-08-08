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

// MaxRequestIDLength is the maximum accepted length for an inbound or explicit
// request id after TrimSpace. Longer values are rejected so a client cannot
// amplify log/storage cost via X-Request-Id / x-request-id.
const MaxRequestIDLength = 128

// NormalizeRequestID trims and validates a request id for safe binding and
// propagation. Accepted charset is [A-Za-z0-9._:-] with length in
// (0, MaxRequestIDLength]. Returns the cleaned id and true when valid;
// otherwise "", false (caller should mint or keep any existing id).
func NormalizeRequestID(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > MaxRequestIDLength {
		return "", false
	}
	for i := 0; i < len(id); i++ {
		if !isAllowedRequestIDByte(id[i]) {
			return "", false
		}
	}
	return id, true
}

func isAllowedRequestIDByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '.' || b == '_' || b == ':' || b == '-':
		return true
	default:
		return false
	}
}

func mintRequestID() string {
	seq := atomic.AddUint64(&requestSeq, 1)
	return fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), seq)
}

// WithRequestID attaches a request ID to the context. If one already exists
// it is preserved. Optional ids[0] supplies an explicit ID (e.g. validate-*).
// Invalid explicit ids are ignored and a fresh id is minted.
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
		if normalized, ok := NormalizeRequestID(ids[0]); ok {
			id = normalized
		}
	}
	if id == "" {
		id = mintRequestID()
	}
	return context.WithValue(ctx, requestIDKey{}, id), id
}

// SetRequestID forces id onto ctx, replacing any existing request ID.
// Empty, whitespace-only, or otherwise invalid ids leave ctx unchanged.
func SetRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, ok := NormalizeRequestID(id)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, normalized)
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
