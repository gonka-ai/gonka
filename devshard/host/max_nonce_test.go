package host

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

func newTestHostWithMaxNonce(t *testing.T, hostIdx int, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, maxNonce uint32) *Host {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 100_000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100_000))
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[hostIdx], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithMaxNonceProvider(devshard.StaticMaxNonce(maxNonce)),
	)
	require.NoError(t, err)
	return h
}

func TestHost_MaxNonce_RejectsActiveWorkPastCap(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	const maxNonce uint32 = 8 // reserve 4 => active cap 4
	h := newTestHostWithMaxNonce(t, 0, hosts, user, maxNonce)

	ctx := context.Background()
	for nonce := uint64(1); nonce <= types.MaxActiveNonce(maxNonce, len(hosts)); nonce++ {
		diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{testutil.StartTx(nonce)})
		_, err := h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff}})
		require.NoError(t, err, "nonce %d", nonce)
	}

	over := types.MaxActiveNonce(maxNonce, len(hosts)) + 1
	diff := testutil.SignDiff(t, user, "escrow-1", over, []*types.DevshardTx{testutil.StartTx(over)})
	_, err := h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff}})
	require.Error(t, err)
	require.True(t, errors.Is(err, types.ErrNonceLimitExceeded))
}

func TestHost_MaxNonce_AllowsFinalizeAfterCap(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	const maxNonce uint32 = 8
	h := newTestHostWithMaxNonce(t, 0, hosts, user, maxNonce)

	ctx := context.Background()
	activeCap := types.MaxActiveNonce(maxNonce, len(hosts))
	for nonce := uint64(1); nonce < activeCap; nonce++ {
		diff := testutil.SignDiff(t, user, "escrow-1", nonce, nil)
		_, err := h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff}})
		require.NoError(t, err, "nonce %d", nonce)
	}
	diff := testutil.SignDiff(t, user, "escrow-1", activeCap, []*types.DevshardTx{testutil.StartTx(activeCap)})
	_, err := h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)

	finalizeNonce := activeCap + 1
	diff = testutil.SignDiff(t, user, "escrow-1", finalizeNonce, []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}},
	})
	_, err = h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)
	require.Equal(t, types.PhaseFinalizing, h.SnapshotState().Phase)
}
