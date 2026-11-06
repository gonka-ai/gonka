package user

import (
	"errors"
	"testing"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func sessionForEffectTest(t *testing.T) *Session {
	t.Helper()
	const escrowID = "escrow-effect"
	hostSigners := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	configuration := types.SessionConfig{
		RefusalTimeout: 1, ExecutionTimeout: 1200, TokenPrice: 1,
		VoteThreshold: 1, ValidationRate: types.DefaultValidationRate,
	}
	verifier := signing.NewSecp256k1Verifier()
	machine, err := state.NewStateMachine(escrowID, configuration, group, 1_000_000, userKey.Address(), verifier,
		testutil.MustMemoryStore(t, escrowID, userKey.Address(), configuration, group, 1_000_000))
	require.NoError(t, err)
	session, err := NewSession(machine, userKey, escrowID, group, make([]HostClient, len(hostSigners)), verifier)
	require.NoError(t, err)
	return session
}

func timeoutDiff(nonce uint64) types.Diff {
	return types.Diff{Txs: []*types.DevshardTx{
		{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{InferenceId: nonce}}},
	}}
}

func TestTimeoutTookEffect(t *testing.T) {
	const nonce = uint64(7)
	tests := []struct {
		name string
		diff types.Diff
		want bool
	}{
		{"the diff carries the timeout", timeoutDiff(nonce), true},
		{"the diff carries a timeout for another nonce", timeoutDiff(nonce + 1), false},
		{"the diff carries nothing", types.Diff{}, false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			session := sessionForEffectTest(t)
			require.Equal(t, testCase.want, session.timeoutTookEffect(testCase.diff, nonce))
		})
	}
}

// A caller separates a settled vote from an unposted one by the sentinel, not by the error text.
func TestTimeoutSettledErrorMarksAnUnpostedVote(t *testing.T) {
	const nonce = uint64(11)

	applied := timeoutSettledError(nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, true)
	unposted := timeoutSettledError(nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, false)

	require.False(t, errors.Is(applied, ErrTimeoutNotApplied),
		"a timeout the diff carried is settled, whatever the error says about it")
	require.True(t, errors.Is(unposted, ErrTimeoutNotApplied),
		"a diff that left without the timeout leaves the vote unposted")
	require.Contains(t, unposted.Error(), "inference 11 timed out",
		"the sentinel is added to the reason, it does not replace it")
}
