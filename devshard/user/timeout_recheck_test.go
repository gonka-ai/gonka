package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

// The caller separates a settled vote from an unsettled one by unwrapping, so a refusal that arrives
// unwrapped is read as success. This pins the wrapping at the source, where it has to happen.
func TestHandleTimeoutReportsAnUnappliedTimeoutAsSuchWhenTheGroupWillNotVote(t *testing.T) {
	const escrowID = "escrow-novotes"
	hostSigners := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	configuration := types.SessionConfig{
		RefusalTimeout: 1, ExecutionTimeout: 1200, TokenPrice: 1,
		VoteThreshold: 1, ValidationRate: types.DefaultValidationRate,
	}
	verifier := signing.NewSecp256k1Verifier()

	// Nil clients: every verifier is unreachable, so the vote can never reach the threshold.
	clients := make([]HostClient, len(hostSigners))
	userMachine, err := state.NewStateMachine(escrowID, configuration, group, 1_000_000, userKey.Address(), verifier,
		testutil.MustMemoryStore(t, escrowID, userKey.Address(), configuration, group, 1_000_000))
	require.NoError(t, err)
	session, err := NewSession(userMachine, userKey, escrowID, group, clients, verifier)
	require.NoError(t, err)

	const nonce = uint64(7)
	session.mu.Lock()
	session.nonceStates[nonce] = &nonceOutcome{}
	session.mu.Unlock()

	_, err = session.HandleTimeout(context.Background(), nonce, time.Now().Add(-time.Hour), nil)

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrTimeoutNotApplied),
		"HandleTimeout() = %v, want an error that unwraps to ErrTimeoutNotApplied", err)
	require.NotNil(t, errors.Unwrap(err),
		"the error must arrive wrapped: an unwrapped one reads as a settled vote")
}
