package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestCollectInferenceDiagEntries_LiveAndSealed(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _, _, _ := newSealTestSM(t, "escrow-diag", hosts, true)

	driveSealInferenceToFinished(t, sm, "escrow-diag", hosts)
	require.NoError(t, sm.SealInference(1))

	_, err := sm.ApplyLocal(4, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 4, PromptHash: []byte("p2"), Model: "llama", InputLength: 10, MaxTokens: 5, StartedAt: 1000,
	})})
	require.NoError(t, err)

	sm.mu.RLock()
	entries := sm.collectInferenceDiagEntriesLocked()
	sm.mu.RUnlock()

	require.Len(t, entries, 2)
	require.Equal(t, uint64(1), entries[0].ID)
	require.True(t, entries[0].Sealed)
	require.Equal(t, uint64(3), entries[0].SealedNonce)
	require.Equal(t, uint64(4), entries[1].ID)
	require.False(t, entries[1].Sealed)
	require.Equal(t, uint8(types.StatusPending), entries[1].Status)
}

func TestIsPostStateRootMismatchError_WrappedHTTPMessage(t *testing.T) {
	err := requireWrappedErr(t, "apply diff nonce 7: post_state_root does not match computed state root: diff aa, computed bb")
	require.True(t, IsPostStateRootMismatchError(err))
}

func requireWrappedErr(t *testing.T, msg string) error {
	t.Helper()
	return &wrappedTestErr{msg: msg}
}

type wrappedTestErr struct{ msg string }

func (e *wrappedTestErr) Error() string { return e.msg }
