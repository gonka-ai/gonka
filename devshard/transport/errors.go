package transport

import (
	"errors"
	"fmt"
	"net/http"
)

// HTTPStatusError is returned by the HTTP transport layer when a host
// responds with a non-200 status. Callers can inspect StatusCode to
// classify the error as fatal (4xx — unlikely to succeed on retry)
// or retryable (selected 4xx, 5xx — transient infrastructure issues).
type HTTPStatusError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *HTTPStatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("http %s %s: status %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("http %s %s: status %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// isRetryableClientStatus reports 4xx codes that should stay on the
// existing refusal/execution timeout retry path instead of failing fast.
func isRetryableClientStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, // 408 — host or proxy timed out; may succeed on retry
		http.StatusTooEarly,         // 425 — RFC 8470 retry-after semantics
		http.StatusTooManyRequests: // 429 — rate limit; backoff then retry
		return true
	default:
		return false
	}
}

// IsFatal reports whether the status indicates a non-retryable client error.
// Fatal 4xx (400, 401, 403, 404, 422, …) are surfaced immediately so
// devshardctl does not wait through refusal/execution timeouts. 5xx and
// network errors are not fatal here — sendAndProcess keeps the deadline path.
func (e *HTTPStatusError) IsFatal() bool {
	if e == nil {
		return false
	}
	if e.StatusCode < http.StatusBadRequest || e.StatusCode >= http.StatusInternalServerError {
		return false
	}
	return !isRetryableClientStatus(e.StatusCode)
}

// IsFatalHTTPError reports whether err wraps an HTTPStatusError with a
// fatal (non-retryable) status code. Returns false for nil errors and
// errors that do not wrap HTTPStatusError.
func IsFatalHTTPError(err error) bool {
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.IsFatal()
}
