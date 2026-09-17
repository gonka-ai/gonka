package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// siblingSM builds a two-validator group where cold owns slots 0 and 1 and
// other owns slot 2, with a resolver that counts bridge calls.
func siblingSM(t *testing.T) (sm *StateMachine, cold, warm *signing.Secp256k1Signer, calls *int) {
	t.Helper()
	cold = testutil.MustGenerateKey(t)
	other := testutil.MustGenerateKey(t)
	warm = testutil.MustGenerateKey(t)
	user := testutil.MustGenerateKey(t)

	group := testutil.MakeMultiSlotGroup([]*signing.Secp256k1Signer{cold, other}, []int{2, 1})
	require.Len(t, group, 3)
	config := testutil.DefaultConfig(len(group))
	verifier := signing.NewSecp256k1Verifier()
	calls = new(int)
	resolver := func(warmAddr, coldAddr string) (bool, error) {
		*calls++
		return warmAddr == warm.Address() && coldAddr == cold.Address(), nil
	}
	sm, err := NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier,
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000),
		WithWarmKeyResolver(resolver))
	require.NoError(t, err)
	return sm, cold, warm, calls
}

// A warm key bound on slot 0 signs for slot 1 of the same validator. The grant
// is per-address, so the binding already on state answers this; apply must not
// call the bridge and must not write a second binding, or two replicas whose
// bridges disagree would seal different rest hashes.
func TestApplyConfirmStart_SiblingWarmKeyNeedsNoBridgeCall(t *testing.T) {
	sm, _, warm, calls := siblingSM(t)
	sm.InjectWarmKeys(map[uint32]string{0: warm.Address()})

	// inference 1 -> group[1 % 3] -> slot 1, cold's other slot.
	_, err := sm.ApplyLocal(1, []*types.DevshardTx{testutil.StartTx(1)})
	require.NoError(t, err)
	rec, ok := sm.Inference(1)
	require.True(t, ok)
	require.Equal(t, uint32(1), rec.ExecutorSlot)

	execSig := testutil.SignExecutorReceipt(t, warm, "escrow-1", 1, testutil.TestPromptHash[:],
		"llama", 100, testutil.TestMaxTokens, 1000, 2000)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{{
		Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
		}},
	}})
	require.NoError(t, err)
	rec, ok = sm.Inference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)

	require.Zero(t, *calls, "sibling binding is already in state; apply must not hit the bridge")
	require.NotContains(t, sm.WarmKeys(), uint32(1), "sibling acceptance must not write a new binding")
}

// Once a slot has its own binding, that binding is exclusive: only the slot's
// cold key or that exact warm key may act for it. A sibling slot's warm key
// must not override it, and the bridge must not be consulted to second-guess
// a binding that is already in state.
func TestApplyConfirmStart_BoundSlotRejectsSiblingWarmKey(t *testing.T) {
	sm, _, warm, calls := siblingSM(t)
	stranger := testutil.MustGenerateKey(t)
	sm.InjectWarmKeys(map[uint32]string{0: warm.Address(), 1: stranger.Address()})

	_, err := sm.ApplyLocal(1, []*types.DevshardTx{testutil.StartTx(1)})
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, warm, "escrow-1", 1, testutil.TestPromptHash[:],
		"llama", 100, testutil.TestMaxTokens, 1000, 2000)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{{
		Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
		}},
	}})
	require.ErrorIs(t, err, types.ErrInvalidExecutorSig)
	require.Zero(t, *calls, "a bound slot is decided from state alone")
	require.Equal(t, stranger.Address(), sm.WarmKeys()[1], "binding must not be replaced")
}

// The cold key always acts for its own slot, even after a warm key is bound.
func TestApplyConfirmStart_ColdKeyActsForBoundSlot(t *testing.T) {
	sm, cold, _, calls := siblingSM(t)
	stranger := testutil.MustGenerateKey(t)
	sm.InjectWarmKeys(map[uint32]string{1: stranger.Address()})

	_, err := sm.ApplyLocal(1, []*types.DevshardTx{testutil.StartTx(1)})
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, cold, "escrow-1", 1, testutil.TestPromptHash[:],
		"llama", 100, testutil.TestMaxTokens, 1000, 2000)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{{
		Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
		}},
	}})
	require.NoError(t, err)
	require.Zero(t, *calls)
}

// HostSignerAllowedAddr answers from state when it can, and otherwise makes
// exactly one bridge call regardless of how many slots the address owns.
func TestHostSignerAllowedAddr_AtMostOneBridgeCall(t *testing.T) {
	sm, cold, warm, calls := siblingSM(t)

	require.True(t, sm.HostSignerAllowedAddr(cold.Address(), cold.Address()))
	require.Zero(t, *calls, "cold key needs no bridge call")

	// Nothing bound yet: cold owns two slots, but only one bridge call.
	require.True(t, sm.HostSignerAllowedAddr(cold.Address(), warm.Address()))
	require.Equal(t, 1, *calls)

	*calls = 0
	sm.InjectWarmKeys(map[uint32]string{0: warm.Address()})
	require.True(t, sm.HostSignerAllowedAddr(cold.Address(), warm.Address()))
	require.Zero(t, *calls, "cached binding must short-circuit the bridge")

	*calls = 0
	stranger := testutil.MustGenerateKey(t)
	require.False(t, sm.HostSignerAllowedAddr(cold.Address(), stranger.Address()))
	require.Equal(t, 1, *calls)
	require.False(t, sm.HostSignerAllowedAddr("", stranger.Address()))
}
