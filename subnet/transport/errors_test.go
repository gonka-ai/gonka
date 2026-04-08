package transport

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPStatusError_IsFatal(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		wantFatal  bool
	}{
		{"400 BadRequest is fatal", http.StatusBadRequest, true},
		{"401 Unauthorized is fatal", http.StatusUnauthorized, true},
		{"403 Forbidden is fatal", http.StatusForbidden, true},
		{"404 NotFound is fatal", http.StatusNotFound, true},
		{"422 Unprocessable is fatal", http.StatusUnprocessableEntity, true},
		{"429 TooManyRequests is retryable", http.StatusTooManyRequests, false},
		{"500 InternalServerError is retryable", http.StatusInternalServerError, false},
		{"502 BadGateway is retryable", http.StatusBadGateway, false},
		{"503 ServiceUnavailable is retryable", http.StatusServiceUnavailable, false},
		{"504 GatewayTimeout is retryable", http.StatusGatewayTimeout, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &HTTPStatusError{StatusCode: tc.statusCode, Path: "/test", Body: "x"}
			require.Equal(t, tc.wantFatal, e.IsFatal())
		})
	}
}

func TestIsFatalHTTPError_UnwrapsViaErrorsAs(t *testing.T) {
	root := &HTTPStatusError{StatusCode: http.StatusForbidden, Path: "/p", Body: "sender not in group"}

	// Direct error.
	require.True(t, IsFatalHTTPError(root))

	// Wrapped with fmt.Errorf %w.
	wrapped := fmt.Errorf("host rejected inference: %w", root)
	require.True(t, IsFatalHTTPError(wrapped))

	// Double wrapped.
	double := fmt.Errorf("outer: %w", wrapped)
	require.True(t, IsFatalHTTPError(double))

	// Non-HTTP error is not fatal.
	require.False(t, IsFatalHTTPError(errors.New("connection reset")))

	// 5xx is not fatal even when wrapped.
	retryable := fmt.Errorf("wrap: %w", &HTTPStatusError{StatusCode: http.StatusBadGateway})
	require.False(t, IsFatalHTTPError(retryable))

	// Nil is not fatal.
	require.False(t, IsFatalHTTPError(nil))
}

func TestHTTPStatusError_ErrorIncludesStatusAndBody(t *testing.T) {
	e := &HTTPStatusError{StatusCode: 403, Path: "/sessions/x/chat/completions", Body: "sender not in group"}
	msg := e.Error()
	require.Contains(t, msg, "403")
	require.Contains(t, msg, "/sessions/x/chat/completions")
	require.Contains(t, msg, "sender not in group")
}
