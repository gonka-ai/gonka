package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestHeartbeatCap_ComposeDropsStaleAckAndReplayMatches(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	store := testutil.MustMemoryStore(t, "escrow-cap", user.Address(), config, group, 10_000_000)

	newSM := func() *StateMachine {
		t.Helper()
		sm, err := NewStateMachine("escrow-cap", config, group, 10_000_000, user.Address(),
			signing.NewSecp256k1Verifier(), store)
		require.NoError(t, err)
		return sm
	}

	gateway := newSM()
	replica := newSM()
	hash := []byte{0xaa}

	// Seed height-sync floor so best-effort compose is floor-ready.
	applyBoth := func(nonce uint64, txs ...*types.DevshardTx) []byte {
		t.Helper()
		d := testutil.SignDiff(t, user, "escrow-cap", nonce, txs)
		rootG, err := gateway.ApplyDiff(d)
		require.NoError(t, err)
		rootR, err := replica.ApplyDiff(d)
		require.NoError(t, err)
		require.Equal(t, rootG, rootR)
		require.NoError(t, store.AppendDiff("escrow-cap", types.DiffRecord{Diff: d, StateHash: rootG}))
		return rootG
	}

	const totalTurns = heightsync.DefaultTurnRetain + 6
	var nonce uint64
	for turn := uint64(1); turn <= totalTurns; turn++ {
		nonce++
		start := nonce
		applyBoth(nonce, snapHeartbeat(start, 50+turn, 3, hash))
		nonce++
		applyBoth(nonce, snapAck(t, hosts[0], start, start, 0, 50+turn, hash))
		nonce++
		applyBoth(nonce, snapAck(t, hosts[1], start, start, 1, 50+turn, hash))
	}

	// Both trackers reach the exact same prunedBelow and bounded heartbeatAt count.
	require.Greater(t, gateway.HeightSyncPrunedBelow(), uint64(1))
	require.Equal(t, gateway.HeightSyncPrunedBelow(), replica.HeightSyncPrunedBelow())
	require.Equal(t, gateway.HeartbeatNonceCount(), replica.HeartbeatNonceCount())
	require.LessOrEqual(t, gateway.HeartbeatNonceCount(), int((heightsync.DefaultTurnRetain+1)*3))

	// Clone carries prunedBelow, and an uncommitted PreviewLocalBestEffort that
	// triggers another prune does not leak into the live tracker after rollback.
	cloned := gateway.HeightSyncCloneTurnTracker()
	require.Equal(t, gateway.HeightSyncPrunedBelow(), cloned.PrunedBelow())

	cutoffBefore := gateway.HeightSyncPrunedBelow()
	hbCountBefore := gateway.HeartbeatNonceCount()
	vd, err := gateway.PreviewLocalBestEffort(nonce+1, []*types.DevshardTx{
		snapHeartbeat(nonce+1, 200, 3, hash),
	})
	require.NoError(t, err)
	require.Len(t, vd.Applied, 1)
	require.Equal(t, cutoffBefore, gateway.HeightSyncPrunedBelow(), "uncommitted preview must not advance live cutoff")
	require.Equal(t, hbCountBefore, gateway.HeartbeatNonceCount(), "uncommitted preview must not mutate live heartbeatAt")

	// Gateway compose drops a stale ack (ref_nonce=1 < prunedBelow) while keeping a fresh heartbeat.
	staleAck := snapAck(t, hosts[2], 1, 1, 2, 200, hash)
	freshHB := snapHeartbeat(nonce+1, 200, 3, hash)
	nonce++
	rootComposed, applied, err := gateway.ApplyLocalBestEffort(nonce, []*types.DevshardTx{staleAck, freshHB})
	require.NoError(t, err)
	require.Len(t, applied, 1, "stale ack must be dropped during gateway best-effort compose")
	require.NotNil(t, applied[0].GetHeartbeat())

	dComposed := testutil.SignDiff(t, user, "escrow-cap", nonce, applied)
	rootReplica, err := replica.ApplyDiff(dComposed)
	require.NoError(t, err)
	require.Equal(t, rootComposed, rootReplica)
	require.NoError(t, store.AppendDiff("escrow-cap", types.DiffRecord{Diff: dComposed, StateHash: rootComposed}))

	// Legacy journal compatibility: a diff written by an older gateway carrying a
	// real stale ack (ref_nonce=1) is accepted by hosts, ApplyLocalPersisted, and
	// snapshot+foldHeightSync with identical post-state roots.
	nonce++
	legacyDiff := testutil.SignDiff(t, user, "escrow-cap", nonce, []*types.DevshardTx{staleAck})
	rootHost, err := replica.ApplyDiff(legacyDiff)
	require.NoError(t, err)
	rootGw, err := gateway.ApplyDiff(legacyDiff)
	require.NoError(t, err)
	require.Equal(t, rootHost, rootGw)
	require.NoError(t, store.AppendDiff("escrow-cap", types.DiffRecord{Diff: legacyDiff, StateHash: rootHost}))

	// Replay the entire journal via ApplyLocalPersisted and verify identical root.
	replayed := newSM()
	diffs, err := store.GetDiffs("escrow-cap", 1, nonce)
	require.NoError(t, err)
	var lastReplayRoot []byte
	for _, rec := range diffs {
		lastReplayRoot, err = replayed.ApplyLocalPersisted(rec.Diff.Nonce, rec.Diff.Txs)
		require.NoError(t, err)
	}
	require.Equal(t, rootHost, lastReplayRoot)
	require.Equal(t, replica.HeightSyncPrunedBelow(), replayed.HeightSyncPrunedBelow())

	// RestoreStateWithFloor folds the journal via foldHeightSync and reaches identical state.
	restored := newSM()
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, replica.ExportHeightSyncFloor())
	require.NoError(t, err)
	require.NoError(t, restored.RestoreStateWithFloor(replica.ExportState(), floor))
	restoredRoot, err := restored.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootHost, restoredRoot)
	require.Equal(t, replica.HeightSyncPrunedBelow(), restored.HeightSyncPrunedBelow())
}
