package registry

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/user"
)

// Test flow:
//  1. Build a registry whose serving factory rebuilds escrow 5 with a session whose Finalize fails.
//  2. Finalize escrow 5 once with a pending completion journal and once with another failure.
//  3. Assert the pending journal reads as busy and still as pending, and the other failure is not busy.
func TestAPendingCompletionJournalFinalizesAsBusy(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		failure  error
		wantBusy bool
	}{
		{name: "pending", failure: fmt.Errorf("%w: nonce 3 has no completion evidence", user.ErrInferenceCompletionPending), wantBusy: true},
		{name: "other", failure: errors.New("collect signatures failed"), wantBusy: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			session := settleableSession(9)
			session.finalizeErr = testCase.failure
			serving := newSessions(map[string]*fakeSession{"5": session})
			registry := New(Deps{ServingSessions: serving.open, Now: fixedClock()})
			registry.ShareChainMaxNonce(1_700)

			err := registry.Finalize(context.Background(), "5")

			require.ErrorIs(t, err, testCase.failure)
			require.Equal(t, testCase.wantBusy, errors.Is(err, chain.ErrDevshardBusy), "errors.Is(%v, ErrDevshardBusy)", err)
		})
	}
}
