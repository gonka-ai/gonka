package mlnodeclient

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrAPINotImplemented indicates that the ML node doesn't support this API endpoint.
// This typically happens when an older version of the ML node is running.
// Can be checked with errors.Is(err, ErrAPINotImplemented)
type ErrAPINotImplemented struct {
	Endpoint   string
	StatusCode int
}

func (e *ErrAPINotImplemented) Error() string {
	return fmt.Sprintf("API endpoint not implemented: %s (HTTP %d)", e.Endpoint, e.StatusCode)
}

func (e *ErrAPINotImplemented) Is(target error) bool {
	_, ok := target.(*ErrAPINotImplemented)
	return ok
}

// NewAPINotImplementedError creates a new ErrAPINotImplemented error
func NewAPINotImplementedError(endpoint string, statusCode int) error {
	return &ErrAPINotImplemented{
		Endpoint:   endpoint,
		StatusCode: statusCode,
	}
}

// StatusError is returned when the MLnode answered with a non-OK HTTP status.
// It carries the status code so callers can distinguish a deterministic
// rejection (the request itself is wrong, e.g. 422) from a transient one (the
// node is busy or temporarily broken, e.g. 409 / 5xx) without pattern-matching
// error strings.
type StatusError struct {
	Op         string // short operation name, e.g. "inference/up"
	StatusCode int
	Body       string // bounded excerpt of the response body
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s failed with status %d", e.Op, e.StatusCode)
	}
	return fmt.Sprintf("%s failed with status %d: %s", e.Op, e.StatusCode, e.Body)
}

// Transient reports whether retrying the same request could plausibly succeed
// later: the node is busy (409 Conflict — vLLM already running or starting),
// rate-limited, timed out, or hit a server-side failure. A 4xx that describes
// the request itself (400/404/422) would fail again identically.
func (e *StatusError) Transient() bool {
	switch e.StatusCode {
	case http.StatusConflict, http.StatusTooManyRequests, http.StatusRequestTimeout:
		return true
	}
	return e.StatusCode >= 500
}

// IsTransientStatus reports whether err is (or wraps) a StatusError whose
// status is worth retrying. False for non-status errors, which callers
// classify separately (a transport error is transient by nature).
func IsTransientStatus(err error) bool {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Transient()
	}
	return false
}

// IsStatusError reports whether err is (or wraps) a StatusError, i.e. the
// MLnode answered but rejected the request — as opposed to a transport-level
// failure where no reply was received at all.
func IsStatusError(err error) bool {
	var statusErr *StatusError
	return errors.As(err, &statusErr)
}
