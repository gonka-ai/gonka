package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/registry"
	"devshard/user"
)

type refusingFinalizeSession struct {
	registry.EscrowSession
	err error
}

func (s refusingFinalizeSession) Finalize(context.Context) error { return s.err }

// Test flow:
//  1. For each Finalize failure, hold an escrow whose session fails with it.
//  2. Send a POST to the escrow's finalize route with the admin key.
//  3. Assert a pending completion journal answers 503 with the Retry-After of the settle tick and any other failure keeps answering 500 with none.
func TestAdminFinalizeAnswersUnavailableWhileTheCompletionJournalIsPending(t *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantRetry  string
	}{
		{
			name:       "pending journal",
			err:        fmt.Errorf("%w: nonce 3 has no completion evidence", user.ErrInferenceCompletionPending),
			wantStatus: http.StatusServiceUnavailable,
			wantRetry:  strconv.Itoa(int(escrow.TickInterval.Seconds())),
		},
		{name: "other failure", err: errors.New("collect signatures failed"), wantStatus: http.StatusInternalServerError},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)
			session, machine := newLiveSession(t)
			live.escrows.sessions[liveEscrowID] = refusingFinalizeSession{EscrowSession: registry.NewSessionHandle(session, machine), err: testCase.err}

			recorder := live.request(t, http.MethodPost, "/devshard/"+liveEscrowID+"/v1/finalize", "", adminHeaders())

			if recorder.Code != testCase.wantStatus {
				t.Fatalf("finalize status = %d (%s), want %d", recorder.Code, recorder.Body.String(), testCase.wantStatus)
			}
			if got := recorder.Header().Get("Retry-After"); got != testCase.wantRetry {
				t.Fatalf("finalize Retry-After = %q, want %q", got, testCase.wantRetry)
			}
		})
	}
}
