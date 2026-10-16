package state

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// Test flow:
//  1. Bind a warm key and point the grant source at three answers in turn: live, ended, unreachable.
//  2. Assert the local checks accept the bound key while the source says live or cannot answer and refuse it
//     once the source says its grant has ended, while the apply path keeps accepting it until a revoke diff.
func TestHostSignerAllowed_RefusesBoundKeyWhoseGrantEnded(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warm := testutil.MustGenerateKey(t)
	answer := func() (bool, error) { return true, nil }
	sm, user := newTestSMWithWarmKey(t, hosts, 100000, func(string, string) (bool, error) { return answer() })
	applyWarmKeyBind(t, sm, user, 1, warm.Address(), 1)

	require.True(t, sm.HostSignerAllowed(1, warm.Address()))
	require.True(t, sm.HostSignerAllowedAddr(hosts[1].Address(), warm.Address()))

	answer = func() (bool, error) { return false, nil }
	require.False(t, sm.HostSignerAllowed(1, warm.Address()), "the source saw the grant end")
	require.False(t, sm.HostSignerAllowedAddr(hosts[1].Address(), warm.Address()))
	require.True(t, sm.WarmKeyGrantEnded(warm.Address(), hosts[1].Address()))

	answer = func() (bool, error) { return false, errors.New("chain down") }
	require.True(t, sm.HostSignerAllowed(1, warm.Address()), "an outage leaves the history's verdict in place")
	require.False(t, sm.WarmKeyGrantEnded(warm.Address(), hosts[1].Address()))

	// The apply path never asks: a confirm by the key still lands until a
	// revoke is sequenced.
	answer = func() (bool, error) { return false, nil }
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{testutil.StartTx(2)}))
	require.NoError(t, err)
	// inference 2 -> slot 2; bind the key there through a sibling? No: slot 1
	// is the bound slot, so use inference 4 on slot 1.
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 3, nil))
	require.NoError(t, err)
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 4, []*types.DevshardTx{testutil.StartTx(4)}))
	require.NoError(t, err)
	execSig := testutil.SignExecutorReceipt(t, warm, "escrow-1", 4, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens, 1000, 1000)
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 5, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 4, ExecutorSig: execSig, ConfirmedAt: 1000,
	})}))
	require.NoError(t, err, "consensus reads the history alone")
}

// Test flow:
//  1. Bind an old key at nonce 1, bind a new key and revoke the old one at nonce 3.
//  2. Assert the address-level check at an explicit nonce follows the history at that nonce: the old key signs
//     nonce 2, not nonce 3; the new key signs nonce 3, not nonce 2.
func TestHostSignerAllowedAddrAt_FollowsTheSignedNonce(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	oldKey, newKey := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	// The grant source knows every key as live: it must not admit a key for
	// a nonce its binding does not cover.
	sm, user := newTestSMWithWarmKey(t, hosts, 100000, func(string, string) (bool, error) { return true, nil })
	applyWarmKeyBind(t, sm, user, 0, oldKey.Address(), 1)
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, nil))
	require.NoError(t, err)
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{
		testutil.WarmKeyBindTx(0, newKey.Address(), 2),
		testutil.WarmKeyRevokeTx(0, oldKey.Address(), 1),
	}))
	require.NoError(t, err)

	cold := hosts[0].Address()
	require.True(t, sm.HostSignerAllowedAddrAt(cold, oldKey.Address(), 2), "a late signature over nonce 2 by the old key still verifies")
	require.False(t, sm.HostSignerAllowedAddrAt(cold, oldKey.Address(), 3))
	require.False(t, sm.HostSignerAllowedAddrAt(cold, newKey.Address(), 2), "the new key did not exist at nonce 2")
	require.True(t, sm.HostSignerAllowedAddrAt(cold, newKey.Address(), 3))
	require.True(t, sm.HostSignerAllowedAddrAt(cold, cold, 1))
	require.False(t, sm.HostSignerAllowedAddr(cold, oldKey.Address()), "the next diff refuses the old key")
	require.False(t, sm.HostSignerAllowedAddrAt(cold, oldKey.Address(), 3), "a live grant does not resurrect a revoked binding")
	stranger := testutil.MustGenerateKey(t)
	require.False(t, sm.HostSignerAllowedAt(0, stranger.Address(), 3), "a signature over a nonce needs a binding at that nonce")
	require.True(t, sm.HostSignerAllowed(0, stranger.Address()), "admission for the next diff may rely on the grant source")
}
