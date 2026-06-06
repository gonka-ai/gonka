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
		{"408 RequestTimeout is retryable", http.StatusRequestTimeout, false},
		{"422 Unprocessable is fatal", http.StatusUnprocessableEntity, true},
		{"425 TooEarly is retryable", http.StatusTooEarly, false},
		{"429 TooManyRequests is retryable", http.StatusTooManyRequests, false},
		{"500 InternalServerError is not fatal", http.StatusInternalServerError, false},
		{"502 BadGateway is not fatal", http.StatusBadGateway, false},
		{"503 ServiceUnavailable is not fatal", http.StatusServiceUnavailable, false},
		{"504 GatewayTimeout is not fatal", http.StatusGatewayTimeout, false},
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

	require.True(t, IsFatalHTTPError(root))

	wrapped := fmt.Errorf("host rejected inference: %w", root)
	require.True(t, IsFatalHTTPError(wrapped))

	double := fmt.Errorf("outer: %w", wrapped)
	require.True(t, IsFatalHTTPError(double))

	require.False(t, IsFatalHTTPError(errors.New("connection reset")))

	retryable := fmt.Errorf("wrap: %w", &HTTPStatusError{StatusCode: http.StatusBadGateway})
	require.False(t, IsFatalHTTPError(retryable))

	require.False(t, IsFatalHTTPError(fmt.Errorf("wrap: %w", &HTTPStatusError{StatusCode: http.StatusRequestTimeout})))

	require.False(t, IsFatalHTTPError(nil))
}

func TestHTTPStatusError_IsFailFast(t *testing.T) {
	cases := []struct {
		name         string
		statusCode   int
		devshardErr  string
		wantFailFast bool
	}{
		{"403 is fail-fast via fatal", http.StatusForbidden, "", true},
		{"501 is fail-fast", http.StatusNotImplemented, "", true},
		{"503 without header is not fail-fast", http.StatusServiceUnavailable, "", false},
		{"503 requests_disabled header", http.StatusServiceUnavailable, DevshardErrorRequestsDisabled, true},
		{"503 initializing header", http.StatusServiceUnavailable, DevshardErrorInitializing, true},
		{"502 is not fail-fast", http.StatusBadGateway, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &HTTPStatusError{
				StatusCode:    tc.statusCode,
				Path:          "/test",
				DevshardError: tc.devshardErr,
			}
			require.Equal(t, tc.wantFailFast, e.IsFailFast())
		})
	}
}

func TestIsFailFastHTTPError_Unwraps(t *testing.T) {
	root := &HTTPStatusError{
		StatusCode:    http.StatusServiceUnavailable,
		DevshardError: DevshardErrorRequestsDisabled,
	}
	require.True(t, IsFailFastHTTPError(fmt.Errorf("host rejected inference: %w", root)))
}

func TestClientHTTPStatusFromError(t *testing.T) {
	err := fmt.Errorf("host rejected inference: %w", &HTTPStatusError{
		StatusCode:    http.StatusServiceUnavailable,
		DevshardError: DevshardErrorRequestsDisabled,
	})
	require.Equal(t, http.StatusServiceUnavailable, ClientHTTPStatusFromError(err))
	require.Equal(t, http.StatusBadGateway, ClientHTTPStatusFromError(fmt.Errorf("timeout")))
}

func TestHTTPStatusError_ErrorIncludesStatusAndBody(t *testing.T) {
	e := &HTTPStatusError{
		Method:     http.MethodPost,
		StatusCode: 403,
		Path:       "/sessions/x/chat/completions",
		Body:       "sender not in group",
	}
	msg := e.Error()
	require.Contains(t, msg, "403")
	require.Contains(t, msg, "/sessions/x/chat/completions")
	require.Contains(t, msg, "sender not in group")
}
