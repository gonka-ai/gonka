package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

// The chain's deadline is minutes away and the host keeps working through it, so the answer that
// makes the vote unnecessary usually lands while HandleTimeout sleeps. Asking anyway spends a
// collection round to be told what the session already knows: verifiers refuse a finished inference.
func TestHandleTimeoutSkipsANonceFinishedWhileItWaited(t *testing.T) {
	const escrowID = "escrow-recheck"
	hostSigners := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	configuration := types.SessionConfig{
		RefusalTimeout: 1, ExecutionTimeout: 1200, TokenPrice: 1,
		VoteThreshold: 1, ValidationRate: types.DefaultValidationRate,
	}
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, len(hostSigners))
	for index, signer := range hostSigners {
		machine, err := state.NewStateMachine(escrowID, configuration, group, 1_000_000, userKey.Address(), verifier,
			testutil.MustMemoryStore(t, escrowID, userKey.Address(), configuration, group, 1_000_000))
		require.NoError(t, err)
		served, err := host.NewHost(machine, signer, stub.NewInferenceEngine(), escrowID, group, nil)
		require.NoError(t, err)
		clients[index] = &InProcessClient{Host: served}
	}
	userMachine, err := state.NewStateMachine(escrowID, configuration, group, 1_000_000, userKey.Address(), verifier,
		testutil.MustMemoryStore(t, escrowID, userKey.Address(), configuration, group, 1_000_000))
	require.NoError(t, err)
	session, err := NewSession(userMachine, userKey, escrowID, group, clients, verifier)
	require.NoError(t, err)

	const nonce = uint64(7)
	session.mu.Lock()
	session.nonceStates[nonce] = &nonceOutcome{finished: true}
	session.mu.Unlock()

	// A dispatch far enough in the past that the refusal deadline has already passed, so the wait
	// returns at once and the re-check is what decides.
	_, err = session.HandleTimeout(context.Background(), nonce, time.Now().Add(-time.Hour), nil)

	require.True(t, errors.Is(err, ErrNonceFinishedWhileWaiting),
		"HandleTimeout() = %v, want it to skip a nonce the host already finished", err)
}

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
