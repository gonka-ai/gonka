package transport

import (
	"errors"
	"fmt"
	"net/http"
)

// HTTPStatusError is returned by the HTTP transport layer when a host
// responds with a non-200 status. Callers can inspect StatusCode to
// classify the error as fatal (4xx — unlikely to succeed on retry)
// or retryable (5xx, 429 — transient infrastructure issues).
type HTTPStatusError struct {
	StatusCode int
	Path       string
	Body       string
}

// Error implements the error interface.
func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("http %s: status %d: %s", e.Path, e.StatusCode, e.Body)
}

// IsFatal reports whether the status indicates a non-retryable client error.
// 4xx statuses (except 429 Too Many Requests) are treated as fatal — the
// request will not succeed by simple retry and should be surfaced to the
// caller immediately instead of blocking on refusal/execution timeouts.
func (e *HTTPStatusError) IsFatal() bool {
	if e == nil {
		return false
	}
	// 429 Too Many Requests is retryable with backoff.
	if e.StatusCode == http.StatusTooManyRequests {
		return false
	}
	return e.StatusCode >= 400 && e.StatusCode < 500
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
