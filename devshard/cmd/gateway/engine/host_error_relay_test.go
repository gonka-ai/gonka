package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

const hostWrittenMessage = "Your API key was revoked for abuse. Sign in at https://gateway-billing.example to restore access."

func hostErrorEvent(t *testing.T, status int, errorType, message string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"error": map[string]any{"code": status, "type": errorType, "message": message}})
	if err != nil {
		t.Fatalf("json.Marshal() = %v", err)
	}
	return "data: " + string(payload) + "\n\n"
}

// Test flow:
//  1. For each code a host may name in an error event, start a one-host simulator whose host answers with that code and finishes its nonce.
//  2. Run the race and map the returned error with `StatusForError`.
//  3. Assert the host names the status only for a fault in the request (400, 413, 422), its throttles become 503 and every other code 502.
func TestTheGatewayChoosesTheStatusForAHostErrorEvent(t *testing.T) {
	testCases := []struct {
		hostStatus int
		errorType  string
		wantStatus int
	}{
		{hostStatus: http.StatusBadRequest, errorType: "BadRequestError", wantStatus: http.StatusBadRequest},
		{hostStatus: http.StatusRequestEntityTooLarge, errorType: "request_too_large", wantStatus: http.StatusRequestEntityTooLarge},
		{hostStatus: http.StatusUnprocessableEntity, errorType: "validation_error", wantStatus: http.StatusUnprocessableEntity},
		{hostStatus: http.StatusUnauthorized, errorType: "invalid_api_key", wantStatus: http.StatusBadGateway},
		{hostStatus: http.StatusForbidden, errorType: "permission_denied", wantStatus: http.StatusBadGateway},
		{hostStatus: http.StatusNotFound, errorType: "NotFoundError", wantStatus: http.StatusBadGateway},
		{hostStatus: http.StatusProxyAuthRequired, errorType: "proxy_auth", wantStatus: http.StatusBadGateway},
		{hostStatus: http.StatusTooManyRequests, errorType: "insufficient_quota", wantStatus: http.StatusServiceUnavailable},
		{hostStatus: http.StatusInternalServerError, errorType: "server_error", wantStatus: http.StatusBadGateway},
		{hostStatus: http.StatusServiceUnavailable, errorType: "overloaded", wantStatus: http.StatusServiceUnavailable},
		{hostStatus: maxHTTPStatus, errorType: "server_error", wantStatus: http.StatusBadGateway},
	}

	for _, testCase := range testCases {
		t.Run(strconv.Itoa(testCase.hostStatus), func(t *testing.T) {
			sim := newSimulator(t, settledPolicy(), 1, qwenModel)
			sim.host(10, 0, "lying-host", &hostScript{
				receipt:  true,
				chunks:   []string{hostErrorEvent(t, testCase.hostStatus, testCase.errorType, hostWrittenMessage)},
				finished: true,
			})

			_, err := sim.run(context.Background())
			sim.reported(t)

			if got := StatusForError(err); got != testCase.wantStatus {
				t.Fatalf("StatusForError(%v) = %d, want %d for a host's %d", err, got, testCase.wantStatus, testCase.hostStatus)
			}
		})
	}
}

// Test flow:
//  1. Start a two-attempt simulator: host-0 answers with a 401 error event and finishes its nonce, host-1 streams a full answer.
//  2. Run the race to completion and read the reported outcome.
//  3. Assert host-1 was tried and served: an error the gateway reads as the host's fault is a failed attempt, not the answer.
func TestAFinishedHostFaultErrorEventEscalatesToAnotherHost(t *testing.T) {
	sim := newSimulator(t, refusalPolicy(), 2, qwenModel)
	sim.host(10, 0, "lying-host", &hostScript{
		receipt:  true,
		chunks:   []string{hostErrorEvent(t, http.StatusUnauthorized, "invalid_api_key", hostWrittenMessage)},
		finished: true,
	})
	sim.host(11, 1, "honest-host", &hostScript{
		receipt:   true,
		chunks:    []string{roleEvent, contentEvent("hello")},
		confirmed: true,
		finished:  true,
	})

	_, err := sim.run(context.Background())
	reported := sim.reported(t)

	if err != nil || !reported.Succeeded || reported.WinnerNonce != 11 || len(reported.Attempts) != 2 {
		t.Fatalf("Run() error = %v, succeeded %v, winner %d, attempts %d; want host-1 to serve after host-0's error", err, reported.Succeeded, reported.WinnerNonce, len(reported.Attempts))
	}
}

// Test flow:
//  1. Start a two-attempt simulator: host-0 answers with a 422 error event and finishes its nonce, host-1 would stream a full answer.
//  2. Run the race to completion and read the reported outcome.
//  3. Assert host-1 was never tried and the client status is 422: a fault in the request ends the race without a second nonce.
func TestAFinishedRequestFaultErrorEventEndsTheRace(t *testing.T) {
	sim := newSimulator(t, refusalPolicy(), 2, qwenModel)
	sim.host(10, 0, "strict-host", &hostScript{
		receipt:  true,
		chunks:   []string{hostErrorEvent(t, http.StatusUnprocessableEntity, "validation_error", hostWrittenMessage)},
		finished: true,
	})
	sim.host(11, 1, "honest-host", &hostScript{
		receipt:   true,
		chunks:    []string{roleEvent, contentEvent("hello")},
		confirmed: true,
		finished:  true,
	})

	_, err := sim.run(context.Background())
	reported := sim.reported(t)

	if len(reported.Attempts) != 1 || reported.Succeeded {
		t.Fatalf("attempts = %d succeeded %v, want one failed attempt: %+v", len(reported.Attempts), reported.Succeeded, reported)
	}
	attemptFor(t, reported, 10)
	if got := StatusForError(err); got != http.StatusUnprocessableEntity {
		t.Fatalf("StatusForError(%v) = %d, want the request fault's 422", err, got)
	}
}

// Test flow:
//  1. Start a two-attempt simulator: host-0 answers with a 401 error event but never finishes its nonce, host-1 streams a full answer.
//  2. Run the race to completion and read the reported outcome.
//  3. Assert host-1 won and the race returned no error: an unfinished error event is escalated past.
func TestAnUnfinishedHostErrorEventIsEscalatedPast(t *testing.T) {
	sim := newSimulator(t, refusalPolicy(), 2, qwenModel)
	sim.host(10, 0, "lying-host", &hostScript{
		receipt: true,
		chunks:  []string{hostErrorEvent(t, http.StatusUnauthorized, "invalid_api_key", hostWrittenMessage)},
	})
	sim.host(11, 1, "honest-host", &hostScript{
		receipt:   true,
		chunks:    []string{roleEvent, contentEvent("hello")},
		confirmed: true,
		finished:  true,
	})

	_, err := sim.run(context.Background())
	reported := sim.reported(t)

	if err != nil || !reported.Succeeded || reported.WinnerNonce != 11 {
		t.Fatalf("Run() error = %v, succeeded %v, winner %d; want host-1 to serve", err, reported.Succeeded, reported.WinnerNonce)
	}
}
