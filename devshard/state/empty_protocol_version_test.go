package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func unversionedStart(id uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: id,
		PromptHash:  []byte("prompt"),
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   testutil.TestMaxTokens,
	}}}
}

func TestApplyDiff_StartInference_EmptyProtocolVersionRejected(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{unversionedStart(1)})
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrProtocolVersionMismatch)
	st := sm.SnapshotState()
	require.Equal(t, uint64(10000), st.Balance)
	require.Nil(t, st.Inferences[1])
}

func TestApplyLocalPersisted_EmptyProtocolVersionStillReplays(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, _ := newTestSM(t, hosts, 10000)

	_, err := sm.ApplyLocalPersisted(1, []*types.DevshardTx{unversionedStart(1)})
	require.NoError(t, err)
	require.NotNil(t, sm.SnapshotState().Inferences[1])
}

func TestApplyDiffPersisted_EmptyProtocolVersionStillReplays(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	writer, user := newTestSM(t, hosts, 10000)
	root, err := writer.ApplyLocalPersisted(1, []*types.DevshardTx{unversionedStart(1)})
	require.NoError(t, err)
	diff := testutil.SignDiffWithRoot(t, user, "escrow-1", 1, []*types.DevshardTx{unversionedStart(1)}, root)

	sm := newTestSMWithUser(t, hosts, 10000, user)
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrProtocolVersionMismatch)
	got, err := sm.ApplyDiffPersisted(diff)
	require.NoError(t, err)
	require.Equal(t, root, got)
}

func TestApplyDiffPersisted_StillChecksUserSignature(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, _ := newTestSM(t, hosts, 10000)
	other := testutil.MustGenerateKey(t)
	diff := testutil.SignDiff(t, other, "escrow-1", 1, []*types.DevshardTx{unversionedStart(1)})
	_, err := sm.ApplyDiffPersisted(diff)
	require.ErrorIs(t, err, types.ErrInvalidUserSig)
}

func newTestSMWithUser(t *testing.T, hosts []*signing.Secp256k1Signer, balance uint64, user *signing.Secp256k1Signer) *StateMachine {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, balance)
	sm, err := NewStateMachine("escrow-1", config, group, balance, user.Address(), signing.NewSecp256k1Verifier(), store)
	require.NoError(t, err)
	return sm
}
