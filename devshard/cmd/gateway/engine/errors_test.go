package engine

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"devshard/transport"
)

// Test flow:
//  1. For each sentinel in the table (`ErrEmptyStream`, `ErrWinnerIncomplete`), wrap it with `fmt.Errorf`.
//  2. Assert `errors.Is` recognizes the wrapped error as its own sentinel.
//  3. Assert `errors.Is` does not recognize it as any of the table's other sentinels.
func TestSentinelsSurviveWrapping(t *testing.T) {
	testCases := []struct {
		name     string
		sentinel error
	}{
		{name: "empty_stream", sentinel: ErrEmptyStream},
		{name: "winner_incomplete", sentinel: ErrWinnerIncomplete},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			wrapped := fmt.Errorf("attempt 7: %w", testCase.sentinel)

			if !errors.Is(wrapped, testCase.sentinel) {
				t.Fatalf("errors.Is(%v) = false, want true", wrapped)
			}
			for _, other := range testCases {
				if other.name == testCase.name {
					continue
				}
				if errors.Is(wrapped, other.sentinel) {
					t.Fatalf("errors.Is(%v, %v) = true, want each sentinel to be distinct", wrapped, other.sentinel)
				}
			}
		})
	}
}

// Test flow:
//  1. Wrap a `HostApplicationError` with `fmt.Errorf`.
//  2. Recover it with `errors.As`.
//  3. Assert the recovered pointer is the original error.
func TestHostApplicationErrorIsRecoverableWhenWrapped(t *testing.T) {
	hostErr := &HostApplicationError{Code: "400", Message: "bad request"}
	wrapped := fmt.Errorf("race failed: %w", hostErr)

	var recovered *HostApplicationError
	if !errors.As(wrapped, &recovered) {
		t.Fatalf("errors.As = false, want the host error back")
	}
	if recovered != hostErr {
		t.Fatalf("recovered = %+v, want the original error", recovered)
	}
}

// Test flow:
//  1. For each table case, call `HTTPStatus()` on the case's `HostApplicationError` (varying code, type, or nil).
//  2. Assert it returns the case's expected HTTP status.
func TestHostApplicationErrorHTTPStatus(t *testing.T) {
	testCases := []struct {
		name       string
		hostErr    *HostApplicationError
		wantStatus int
	}{
		{name: "code_outside_the_error_range_is_ignored", hostErr: &HostApplicationError{Code: "200", Type: "BadRequestError"}, wantStatus: http.StatusBadRequest},
		{name: "numeric_code", hostErr: &HostApplicationError{Code: "429"}, wantStatus: http.StatusTooManyRequests},
		{name: "non_numeric_code_with_bad_request_type", hostErr: &HostApplicationError{Code: "invalid_request_error", Type: "BadRequestError"}, wantStatus: http.StatusBadRequest},
		{name: "unknown_shape", hostErr: &HostApplicationError{Message: "something broke"}, wantStatus: http.StatusBadGateway},
		{name: "nil", hostErr: nil, wantStatus: http.StatusBadGateway},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.hostErr.HTTPStatus(); got != testCase.wantStatus {
				t.Fatalf("status = %d, want %d", got, testCase.wantStatus)
			}
		})
	}
}

// Test flow:
//  1. For each table case, call `Error()` on the case's `HostApplicationError`.
//  2. Assert it returns the case's expected message, in priority order: explicit message, then payload, then a fallback string.
func TestHostApplicationErrorMessage(t *testing.T) {
	testCases := []struct {
		name        string
		hostErr     *HostApplicationError
		wantMessage string
	}{
		{name: "message_wins", hostErr: &HostApplicationError{Message: "context length exceeded", Payload: `{"error":{}}`}, wantMessage: "context length exceeded"},
		{name: "payload_when_no_message", hostErr: &HostApplicationError{Payload: `{"error":{}}`}, wantMessage: `{"error":{}}`},
		{name: "fallback", hostErr: &HostApplicationError{}, wantMessage: "host application error"},
		{name: "nil", hostErr: nil, wantMessage: "host application error"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.hostErr.Error(); got != testCase.wantMessage {
				t.Fatalf("message = %q, want %q", got, testCase.wantMessage)
			}
		})
	}
}

// Test flow:
//  1. Wrap a `transport.UpstreamStatusError` with `fmt.Errorf`.
//  2. Recover its status via `UpstreamStatus`.
//  3. Assert the recovered status matches and `ok` is true.
//  4. Assert `UpstreamStatus` reports `ok=false` for an error that carries no upstream status.
func TestUpstreamStatusRecovery(t *testing.T) {
	wrapped := fmt.Errorf("send: %w", &transport.UpstreamStatusError{Path: "/v1/chat/completions", StatusCode: http.StatusTooManyRequests})

	status, ok := UpstreamStatus(wrapped)
	if !ok || status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, ok = %v, want 429, true", status, ok)
	}
	if _, ok := UpstreamStatus(ErrEmptyStream); ok {
		t.Fatalf("ok = true for an error carrying no upstream status, want false")
	}
}

// Test flow:
//  1. For each table case's error (nil, host application errors, wrapped upstream status errors, sentinel errors, an unknown error), call `StatusForError`.
//  2. Assert it returns the case's expected HTTP status.
func TestStatusForError(t *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "success", err: nil, wantStatus: http.StatusOK},
		{name: "host_application_error", err: &HostApplicationError{Code: "400"}, wantStatus: http.StatusBadRequest},
		{name: "wrapped_host_application_error", err: fmt.Errorf("race: %w", &HostApplicationError{Code: "404"}), wantStatus: http.StatusNotFound},
		{name: "upstream_throttled", err: &transport.UpstreamStatusError{StatusCode: http.StatusTooManyRequests}, wantStatus: http.StatusTooManyRequests},
		{name: "upstream_unavailable", err: &transport.UpstreamStatusError{StatusCode: http.StatusServiceUnavailable}, wantStatus: http.StatusTooManyRequests},
		{name: "upstream_server_error", err: &transport.UpstreamStatusError{StatusCode: http.StatusInternalServerError}, wantStatus: http.StatusBadGateway},
		{name: "empty_stream", err: ErrEmptyStream, wantStatus: http.StatusBadGateway},
		{name: "winner_incomplete", err: ErrWinnerIncomplete, wantStatus: http.StatusBadGateway},
		{name: "truncated_stream", err: transport.ErrSSEStreamTruncated, wantStatus: http.StatusBadGateway},
		{name: "hosts_unavailable", err: ErrHostsUnavailable, wantStatus: http.StatusServiceUnavailable},
		{name: "wrapped_hosts_unavailable", err: fmt.Errorf("race: %w", ErrHostsUnavailable), wantStatus: http.StatusServiceUnavailable},
		{name: "unknown", err: errors.New("something else"), wantStatus: http.StatusBadGateway},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := StatusForError(testCase.err); got != testCase.wantStatus {
				t.Fatalf("status = %d, want %d", got, testCase.wantStatus)
			}
		})
	}
}

// Test flow:
//  1. Wrap a `HostApplicationError` (400) together with a `transport.UpstreamStatusError` (429) in the same error chain.
//  2. Call `StatusForError` on the combined error.
//  3. Assert the host's own 400 status wins over the wrapped upstream status.
func TestHostApplicationErrorWinsOverAWrappedUpstreamStatus(t *testing.T) {
	err := fmt.Errorf("race: %w", &HostApplicationError{
		Code:    "400",
		Message: "context length exceeded",
	})

	if got := StatusForError(fmt.Errorf("%w: %w", err, &transport.UpstreamStatusError{StatusCode: http.StatusTooManyRequests})); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want the host's own status 400", got)
	}
}
