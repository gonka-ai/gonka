package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type stubCatchUpPeer struct {
	leader         *Host
	maxServeNonce  uint64
	snapshotHits   atomic.Int32
	diffPageHits   atomic.Int32
	mutateSnapshot func(*PeerSnapshot)
}

func (p *stubCatchUpPeer) GetSnapshot(_ context.Context, maxNonce uint64) (*PeerSnapshot, error) {
	p.snapshotHits.Add(1)
	if p.maxServeNonce > 0 && (maxNonce == 0 || p.maxServeNonce < maxNonce) {
		maxNonce = p.maxServeNonce
	}
	snap, err := p.leader.ServeGetSnapshot(maxNonce)
	if err != nil {
		return nil, err
	}
	cloned := &PeerSnapshot{
		Nonce:    snap.Nonce,
		Snapshot: append([]byte(nil), snap.Snapshot...),
	}
	if snap.AnchorDiff != nil {
		cp := *snap.AnchorDiff
		cloned.AnchorDiff = &cp
	}
	if snap.FloorRefDiff != nil {
		cp := *snap.FloorRefDiff
		cloned.FloorRefDiff = &cp
	}
	if len(snap.Signatures) > 0 {
		cloned.Signatures = make(map[uint32][]byte, len(snap.Signatures))
		for k, v := range snap.Signatures {
			cloned.Signatures[k] = append([]byte(nil), v...)
		}
	}
	if p.mutateSnapshot != nil {
		p.mutateSnapshot(cloned)
	}
	return cloned, nil
}

func (p *stubCatchUpPeer) GetDiffPages(_ context.Context, fromNonce, toNonce uint64, fn func([]types.Diff) error) error {
	p.diffPageHits.Add(1)
	if toNonce < fromNonce {
		return nil
	}
	cur := fromNonce
	for cur <= toNonce {
		end := cur + storage.DiffPageMaxNonces - 1
		if end > toNonce {
			end = toNonce
		}
		recs, err := p.leader.store.GetDiffs("escrow-1", cur, end)
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			break
		}
		diffs := make([]types.Diff, len(recs))
		for i, r := range recs {
			diffs[i] = r.Diff
		}
		if err := fn(diffs); err != nil {
			return err
		}
		last := recs[len(recs)-1].Nonce
		if last < cur {
			break
		}
		cur = last + 1
	}
	return nil
}

func (p *stubCatchUpPeer) GetSignatures(_ context.Context, nonce uint64) (map[uint32][]byte, error) {
	return p.leader.store.GetSignatures("escrow-1", nonce)
}

func newStoredTestHost(
	t *testing.T,
	hostIdx int,
	hostSigners []*signing.Secp256k1Signer,
	userSigner *signing.Secp256k1Signer,
	balance uint64,
	extraOpts ...HostOption,
) (*Host, *storage.Memory) {
	t.Helper()
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(len(hostSigners))
	verifier := signing.NewSecp256k1Verifier()

	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		Config:         config,
		Group:          group,
		InitialBalance: balance,
		CreatorAddr:    userSigner.Address(),
	}))

	sm, err := state.NewStateMachine("escrow-1", config, group, balance, userSigner.Address(), verifier, store)
	require.NoError(t, err)

	opts := append([]HostOption{WithGrace(200), WithStorage(store), WithVerifier(verifier)}, extraOpts...)
	h, err := NewHost(sm, hostSigners[hostIdx], stub.NewInferenceEngine(), "escrow-1", group, nil, opts...)
	require.NoError(t, err)
	return h, store
}

func stateSigData(t *testing.T, escrowID string, nonce uint64, root []byte) []byte {
	t.Helper()
	b, err := proto.Marshal(&types.StateSignatureContent{
		StateRoot: root,
		EscrowId:  escrowID,
		Nonce:     nonce,
	})
	require.NoError(t, err)
	return b
}

// advanceLeadersWithQuorumSigs advances leader hosts cleanly to targetNonce,
// signing each nonce root with the user (PostStateRoot) and all group members,
// and optionally saving a durable snapshot at snapNonce on leaders[0].
func advanceLeadersWithQuorumSigs(
	t *testing.T,
	leaders []*Host,
	hostSigners []*signing.Secp256k1Signer,
	userSigner *signing.Secp256k1Signer,
	balance uint64,
	targetNonce uint64,
	snapNonce uint64,
) []byte {
	t.Helper()
	ctx := context.Background()
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(len(hostSigners))
	verifier := signing.NewSecp256k1Verifier()
	userSM, err := state.NewStateMachine(
		"escrow-1",
		config,
		group,
		balance,
		userSigner.Address(),
		verifier,
		testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, balance),
	)
	require.NoError(t, err)

	var lastRoot []byte
	for n := uint64(1); n <= targetNonce; n++ {
		txs := []*types.DevshardTx{testutil.StartTx(n)}
		expectedRoot, applyErr := userSM.ApplyLocal(n, txs)
		require.NoError(t, applyErr)
		diff := testutil.SignDiffWithRoot(t, userSigner, "escrow-1", n, txs, expectedRoot)
		for _, leader := range leaders {
			resp, err := leader.HandleRequest(ctx, HostRequest{
				Nonce:   n,
				Diffs:   []types.Diff{diff},
				Payload: defaultPayload(),
			})
			require.NoError(t, err)
			lastRoot = resp.StateHash
		}
		sigData := stateSigData(t, "escrow-1", n, lastRoot)
		for slot, hs := range hostSigners {
			sig, err := hs.Sign(sigData)
			require.NoError(t, err)
			for _, leader := range leaders {
				require.NoError(t, leader.AccumulateGossipSig(n, lastRoot, sig, uint32(slot)))
			}
		}
		if snapNonce > 0 && n == snapNonce {
			snapBytes, err := MarshalStateSnapshotWithCommitted(
				leaders[0].sm.ExportState(),
				leaders[0].sm.ExportCommittedEntries(),
				leaders[0].sm.ExportSealedNonces(),
				leaders[0].sm.ExportHeightSyncFloor(),
			)
			require.NoError(t, err)
			require.NoError(t, leaders[0].store.SaveSnapshot("escrow-1", snapNonce, snapBytes))
		}
	}
	return lastRoot
}

func TestHost_CatchUpToNonce_ShortGapUsesDiffPagesOnly(t *testing.T) {
	hostSigners := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	userSigner := testutil.MustGenerateKey(t)

	h0, _ := newStoredTestHost(t, 0, hostSigners, userSigner, 1_000_000)
	h1, _ := newStoredTestHost(t, 1, hostSigners, userSigner, 1_000_000)
	h2, _ := newStoredTestHost(t, 2, hostSigners, userSigner, 1_000_000)

	peer1 := &stubCatchUpPeer{leader: h1}
	h0.SetCatchUpSource(func(_ context.Context, slot int) (PeerCatchUpClient, error) {
		if slot == 1 {
			return peer1, nil
		}
		return nil, nil
	})

	tipRoot := advanceLeadersWithQuorumSigs(t, []*Host{h1, h2}, hostSigners, userSigner, 1_000_000, 15, 10)

	// Finalize-style nil-diff probe at nonce 15 triggers peer catch-up via diff pages only.
	resp, err := h0.HandleRequest(context.Background(), HostRequest{Nonce: 15})
	require.NoError(t, err)
	require.Equal(t, uint64(15), h0.LatestNonce())
	require.Equal(t, tipRoot, resp.StateHash)
	require.Equal(t, int32(0), peer1.snapshotHits.Load(), "short gap <= SnapshotCatchUpMinGap must not fetch snapshot")
	require.GreaterOrEqual(t, peer1.diffPageHits.Load(), int32(1))

	verifier := signing.NewSecp256k1Verifier()
	sigData := stateSigData(t, "escrow-1", 15, tipRoot)
	recovered, err := verifier.RecoverAddress(sigData, resp.StateSig)
	require.NoError(t, err)
	require.Equal(t, hostSigners[0].Address(), recovered)
}

func TestHost_CatchUpToNonce_WideGapAdoptsVerifiedSnapshotAndTailDiffs(t *testing.T) {
	hostSigners := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	userSigner := testutil.MustGenerateKey(t)

	h0, store0 := newStoredTestHost(t, 0, hostSigners, userSigner, 10_000_000)
	h1, _ := newStoredTestHost(t, 1, hostSigners, userSigner, 10_000_000)
	h2, _ := newStoredTestHost(t, 2, hostSigners, userSigner, 10_000_000)

	// Cap GetSnapshot at 85 so h0 adopts the disk snapshot at 85 AND exercises tail replay 86..90!
	peer1 := &stubCatchUpPeer{leader: h1, maxServeNonce: 85}
	h0.SetCatchUpSource(func(_ context.Context, slot int) (PeerCatchUpClient, error) {
		if slot == 1 {
			return peer1, nil
		}
		return nil, nil
	})

	tipRoot := advanceLeadersWithQuorumSigs(t, []*Host{h1, h2}, hostSigners, userSigner, 10_000_000, 90, 85)

	resp, err := h0.HandleRequest(context.Background(), HostRequest{Nonce: 90})
	require.NoError(t, err)
	require.Equal(t, uint64(90), h0.LatestNonce())
	require.Equal(t, tipRoot, resp.StateHash)
	require.Equal(t, int32(1), peer1.snapshotHits.Load(), "wide gap > SnapshotCatchUpMinGap must use GetSnapshot")
	require.Equal(t, int32(1), peer1.diffPageHits.Load(), "tail 86..90 must be fetched in one diff page")

	// Gossip sig accumulation at caught-up tip succeeds against sparse post-snapshot journal.
	sigData := stateSigData(t, "escrow-1", 90, tipRoot)
	sig1, err := hostSigners[1].Sign(sigData)
	require.NoError(t, err)
	require.NoError(t, h0.AccumulateGossipSig(90, tipRoot, sig1, 1))

	// Cold-restart recovery (mirroring cmd/devshardd/session/manager.go) succeeds
	// from store0's sparse journal (nonces 85..90 only, nonces 1..84 absent!).
	meta, err := store0.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(90), meta.LatestNonce)

	snapNonce, snapBytes, err := store0.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(85), snapNonce)

	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(len(hostSigners))
	verifier := signing.NewSecp256k1Verifier()
	restartedSM, err := state.NewStateMachine("escrow-1", config, group, 10_000_000, userSigner.Address(), verifier, store0)
	require.NoError(t, err)

	snapState, committed, sealed, floorProto, err := UnmarshalStateSnapshotWithCommitted(snapBytes)
	require.NoError(t, err)
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, floorProto)
	require.NoError(t, err)
	require.NoError(t, restartedSM.RestoreStateWithFloor(snapState, floor))
	require.NoError(t, restartedSM.RestoreCommittedEntries(committed))
	restartedSM.RestoreSealedNonces(sealed)

	anchorRecs, err := store0.GetDiffs("escrow-1", snapNonce, snapNonce)
	require.NoError(t, err)
	require.Len(t, anchorRecs, 1)
	restoredAnchorRoot, err := restartedSM.ComputeStateRoot()
	require.NoError(t, err)
	require.True(t, bytes.Equal(restoredAnchorRoot, anchorRecs[0].StateHash))

	err = storage.ReadDiffPages(store0, "escrow-1", snapNonce+1, meta.LatestNonce, func(page []types.DiffRecord) error {
		for _, rec := range page {
			restartedSM.InjectWarmKeys(rec.WarmKeyDelta)
			root, applyErr := restartedSM.ApplyLocalPersisted(rec.Nonce, rec.Txs)
			if applyErr != nil {
				return applyErr
			}
			require.Equal(t, rec.StateHash, root)
		}
		return nil
	})
	require.NoError(t, err)
	finalRoot, err := restartedSM.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, tipRoot, finalRoot)
}

func TestHost_CatchUpToNonce_RejectsTamperedSnapshotAndFallsBackToDiffPages(t *testing.T) {
	hostSigners := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	userSigner := testutil.MustGenerateKey(t)

	h0, _ := newStoredTestHost(t, 0, hostSigners, userSigner, 10_000_000)
	h1, _ := newStoredTestHost(t, 1, hostSigners, userSigner, 10_000_000)
	h2, _ := newStoredTestHost(t, 2, hostSigners, userSigner, 10_000_000)

	tamperedPeer := &stubCatchUpPeer{
		leader: h1,
		mutateSnapshot: func(ps *PeerSnapshot) {
			st, committed, sealed, floor, err := UnmarshalStateSnapshotWithCommitted(ps.Snapshot)
			if err == nil && st != nil {
				st.Balance += 999_999
				if mutated, mErr := MarshalStateSnapshotWithCommitted(st, committed, sealed, floor); mErr == nil {
					ps.Snapshot = mutated
				}
			}
		},
	}
	h0.SetCatchUpSource(func(_ context.Context, slot int) (PeerCatchUpClient, error) {
		if slot == 1 {
			return tamperedPeer, nil
		}
		return nil, nil
	})

	tipRoot := advanceLeadersWithQuorumSigs(t, []*Host{h1, h2}, hostSigners, userSigner, 10_000_000, 75, 70)

	resp, err := h0.HandleRequest(context.Background(), HostRequest{Nonce: 75})
	require.NoError(t, err)
	require.Equal(t, uint64(75), h0.LatestNonce())
	require.Equal(t, tipRoot, resp.StateHash)
	require.Equal(t, int32(1), tamperedPeer.snapshotHits.Load())
	require.GreaterOrEqual(t, tamperedPeer.diffPageHits.Load(), int32(1))
}

func TestHost_HandleRequest_NilPayloadRecoversStartedFinishFromStorage(t *testing.T) {
	hostSigners := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	userSigner := testutil.MustGenerateKey(t)

	// Inference ID 1 maps to executor slot 1 (1 % 3 == 1).
	h1Leader, _ := newStoredTestHost(t, 1, hostSigners, userSigner, 1_000_000)
	diff1 := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp1, err := h1Leader.HandleRequest(context.Background(), HostRequest{
		Nonce:   1,
		Diffs:   []types.Diff{diff1},
		Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp1.ExecutionJob)
	confirmTx := findMempoolConfirm(h1Leader.MempoolTxs())
	require.NotNil(t, confirmTx)

	confirmDiff := testutil.SignDiff(t, userSigner, "escrow-1", 2, []*types.DevshardTx{confirmTx})
	_, err = h1Leader.HandleRequest(context.Background(), HostRequest{
		Nonce: 2,
		Diffs: []types.Diff{confirmDiff},
	})
	require.NoError(t, err)

	// Simulate Host 1 failover/DB loss: fresh state machine at nonce 0, empty mempool,
	// with only the completed execution artifact preserved in payload storage.
	storedBody := []byte(`{"id":"recovered-from-payload-store"}`)
	storedHash := sha256.Sum256(storedBody)
	engine := &recoveringEngine{
		stored: &devshard.ExecuteResult{
			ResponseHash: storedHash[:],
			InputTokens:  11,
			OutputTokens: 29,
			ResponseBody: storedBody,
		},
	}
	h1Failover, _ := newStoredTestHost(
		t,
		1,
		hostSigners,
		userSigner,
		1_000_000,
		WithStoredResponse(func(context.Context, string, uint64, uint64) ([]byte, error) {
			return storedBody, nil
		}),
	)
	h1Failover.engine = engine

	peer := &stubCatchUpPeer{leader: h1Leader}
	h1Failover.SetCatchUpSource(func(_ context.Context, slot int) (PeerCatchUpClient, error) {
		if slot == 2 {
			return peer, nil
		}
		return nil, nil
	})

	// Finalize resume sends HostRequest{Nonce: 2, Diffs: nil, Payload: nil}.
	resp, err := h1Failover.HandleRequest(context.Background(), HostRequest{Nonce: 2})
	require.NoError(t, err)
	require.Equal(t, uint64(2), h1Failover.LatestNonce())
	require.Nil(t, resp.ExecutionJob, "nil-payload probe must recover synchronously without returning deferred ExecutionJob")
	require.Zero(t, engine.runs, "RecoveryStoredOnly must never re-run the model")

	finish := findMempoolFinish(resp.Mempool)
	require.NotNil(t, finish, "stored finish must be recovered and returned in HostResponse.Mempool")
	require.Equal(t, uint64(1), finish.GetFinishInference().InferenceId)
	require.Equal(t, storedHash[:], finish.GetFinishInference().ResponseHash)
	require.Equal(t, uint64(11), finish.GetFinishInference().InputTokens)
	require.Equal(t, uint64(29), finish.GetFinishInference().OutputTokens)
	require.Equal(t, uint32(1), finish.GetFinishInference().ExecutorSlot)
}
