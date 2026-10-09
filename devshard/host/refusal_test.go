package host

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type refusalFixture struct {
	p      *types.RefusalPackage
	states map[uint64]*types.EscrowState
	diffs  []types.Diff
	hosts  []*signing.Secp256k1Signer
	user   *signing.Secp256k1Signer
}

func newRefusalFixture(t *testing.T) refusalFixture {
	t.Helper()
	f := refusalFixture{states: map[uint64]*types.EscrowState{}, user: testutil.MustGenerateKey(t)}
	for i := 0; i < 3; i++ {
		f.hosts = append(f.hosts, testutil.MustGenerateKey(t))
	}
	group := testutil.MakeGroup(f.hosts)
	cfg := testutil.DefaultConfig(3)
	cfg.CreateDevshardFee = 7
	store := testutil.MustMemoryStore(t, "escrow-1", f.user.Address(), cfg, group, 10000)
	sm, err := state.NewStateMachine("escrow-1", cfg, group, 10000, f.user.Address(), signing.NewSecp256k1Verifier(), store)
	require.NoError(t, err)
	f.states[0] = sm.ExportState()
	sigs := map[uint64]map[uint32][]byte{}
	for n := uint64(1); n <= 5; n++ {
		var txs []*types.DevshardTx
		if n == 1 {
			txs = []*types.DevshardTx{testutil.StartTx(1)}
		}
		root, err := sm.ApplyLocal(n, txs)
		require.NoError(t, err)
		f.diffs = append(f.diffs, testutil.SignDiffWithRoot(t, f.user, "escrow-1", n, txs, root))
		f.states[n] = sm.ExportState()
		if n >= 2 && n <= 4 {
			data, err := proto.Marshal(&types.StateSignatureContent{EscrowId: "escrow-1", Nonce: n, StateRoot: root})
			require.NoError(t, err)
			sig, err := f.hosts[n-2].Sign(data)
			require.NoError(t, err)
			sigs[n] = map[uint32][]byte{uint32(n - 2): sig}
		}
	}
	// The later signatures reach quorum without the signature at N.
	content, err := proto.Marshal(&types.StateSignatureContent{EscrowId: "escrow-1", Nonce: 3, StateRoot: f.diffs[2].PostStateRoot})
	require.NoError(t, err)
	sigs[3][0], err = f.hosts[0].Sign(content)
	require.NoError(t, err)
	snap := f.states[2]
	if len(snap.SealedAcc) == 0 {
		snap.SealedAcc = make([]byte, 32)
	}
	data, err := types.MarshalStateSnapshotProto(snap, nil, nil)
	require.NoError(t, err)
	f.p = &types.RefusalPackage{EscrowID: "escrow-1", Version: snap.StateRootAndProtocolVersion, N: 2, T: 4, Snapshot: data, Diffs: f.diffs[2:4], Signatures: sigs}
	return f
}

func (f refusalFixture) host(t *testing.T, n uint64, store storage.Storage) *Host {
	t.Helper()
	st := f.states[0]
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Version: st.StateRootAndProtocolVersion, EpochID: 1, Config: st.Config, Group: st.Group, CreatorAddr: f.user.Address(), InitialBalance: 10000}))
	sm, err := state.NewStateMachine("escrow-1", st.Config, st.Group, 10000, f.user.Address(), signing.NewSecp256k1Verifier(), store)
	require.NoError(t, err)
	sm.RestoreState(f.states[n])
	for _, d := range f.diffs[:n] {
		require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: d, StateHash: d.PostStateRoot}))
	}
	h, err := NewHost(sm, f.hosts[1], stub.NewInferenceEngine(), "escrow-1", st.Group, nil, WithStorage(store), WithVerifier(signing.NewSecp256k1Verifier()))
	require.NoError(t, err)
	return h
}

func TestRefusalCatchUpPositions(t *testing.T) {
	f := newRefusalFixture(t)
	for _, n := range []uint64{0, 2, 3, 4, 5} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := &countingGetDiffsStore{Storage: storage.NewMemory()}
			h := f.host(t, n, s)
			p := *f.p
			if n >= p.N {
				p.Snapshot = []byte("deliberately undecodable")
			}
			require.NoError(t, h.CatchUpForChallenge(context.Background(), &p, 1, testPayload()))
			require.Equal(t, max(n, p.T), h.LatestNonce())
			require.Zero(t, s.gets.Load())
			got, err := h.sm.ComputeStateRoot()
			require.NoError(t, err)
			want, err := state.SnapshotRoot(f.states[max(n, p.T)])
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

func TestRefusalProofRejectsWithoutSideEffects(t *testing.T) {
	for _, name := range []string{"valid", "signature", "quorum", "gap", "root", "snapshot", "binding", "phase", "outside", "too_long", "target", "warm_key"} {
		t.Run(name, func(t *testing.T) {
			f := newRefusalFixture(t)
			s := storage.NewMemory()
			h := f.host(t, 1, s)
			switch name {
			case "signature":
				f.p.Signatures[2][0] = []byte("bad")
			case "quorum":
				delete(f.p.Signatures, 4)
			case "gap":
				f.p.Diffs[0].Nonce++
			case "root":
				f.p.Diffs[0] = testutil.SignDiffWithRoot(t, f.user, "escrow-1", f.p.Diffs[0].Nonce, f.p.Diffs[0].Txs, make([]byte, 32))
			case "snapshot":
				f.p.Snapshot = []byte("bad")
			case "binding":
				f.p.EscrowID = "other"
			case "phase":
				st := f.states[2]
				st.Phase = types.PhaseSettlement
				f.p.Snapshot, _ = types.MarshalStateSnapshotProto(st, nil, nil)
			case "outside":
				f.p.Signatures[5] = f.p.Signatures[2]
			case "too_long":
				f.p.T = 1003
			case "target":
				st := f.states[0]
				st.SealedAcc = make([]byte, 32)
				f.p.N = 0
				f.p.T = 0
				f.p.Diffs = nil
				f.p.Snapshot, _ = types.MarshalStateSnapshotProto(st, nil, nil)
				root, _ := state.SnapshotRoot(st)
				content, _ := proto.Marshal(&types.StateSignatureContent{EscrowId: "escrow-1", StateRoot: root})
				f.p.Signatures = map[uint64]map[uint32][]byte{0: {}}
				for i, s := range f.hosts {
					sig, e := s.Sign(content)
					require.NoError(t, e)
					f.p.Signatures[0][uint32(i)] = sig
				}
			case "warm_key":
				st := f.states[2]
				st.WarmKeys[0] = f.user.Address()
				f.p.Snapshot, _ = types.MarshalStateSnapshotProto(st, nil, nil)
			}
			before, _ := h.sm.ComputeStateRoot()
			err := h.VerifyRefusalPackage(f.p, 1, testPayload())
			if name == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			after, _ := h.sm.ComputeStateRoot()
			require.Equal(t, before, after)
			meta, err := s.GetSessionMeta("escrow-1")
			require.NoError(t, err)
			require.Equal(t, uint64(1), meta.LatestNonce)
		})
	}
}

func TestRefusalImportHAContiguousWriteAndRestart(t *testing.T) {
	f := newRefusalFixture(t)
	s := storage.NewMemory()
	standby := f.host(t, 1, s)
	require.NoError(t, standby.ImportVerifiedSnapshot(f.states[4]))
	// Restore a stale standby without rewriting its already imported journal.
	staleSM, err := state.NewStateMachine("escrow-1", f.states[0].Config, f.states[0].Group, 10000, f.user.Address(), signing.NewSecp256k1Verifier(), s)
	require.NoError(t, err)
	staleSM.RestoreState(f.states[1])
	stale, err := NewHost(staleSM, f.hosts[1], stub.NewInferenceEngine(), "escrow-1", f.states[0].Group, nil, WithStorage(s))
	require.NoError(t, err)
	stale.mu.Lock()
	err = stale.applyAndPersistReconciling(context.Background(), f.diffs[1])
	stale.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, uint64(4), stale.LatestNonce())
	rows, err := s.GetDiffs("escrow-1", 2, 4)
	require.NoError(t, err)
	require.Empty(t, rows)
	n, data, err := s.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(4), n)
	restored, err := UnmarshalStateSnapshot(data)
	require.NoError(t, err)
	root, err := state.SnapshotRoot(restored)
	require.NoError(t, err)
	require.Equal(t, f.diffs[3].PostStateRoot, root)
	data[len("devshard-snapshot-root-v1\x00")] ^= 1
	_, err = UnmarshalStateSnapshot(data)
	require.Error(t, err)
	stale.mu.Lock()
	err = stale.applyAndPersistReconciling(context.Background(), f.diffs[4])
	stale.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, uint64(5), stale.LatestNonce())
}

type failedSnapshotStore struct{ storage.Storage }

func (s *failedSnapshotStore) ImportSnapshot(string, uint64, []byte) error {
	return fmt.Errorf("injected persistence failure")
}

func TestRefusalImportFailureLeavesLiveState(t *testing.T) {
	f := newRefusalFixture(t)
	s := &failedSnapshotStore{Storage: storage.NewMemory()}
	h := f.host(t, 1, s)
	before, err := h.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Error(t, h.CatchUpForChallenge(context.Background(), f.p, 1, testPayload()))
	after, err := h.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, before, after)
	meta, err := s.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1), meta.LatestNonce)
}

func TestRefusalImportPreservesOutstandingWork(t *testing.T) {
	f := newRefusalFixture(t)
	h := f.host(t, 1, storage.NewMemory())
	pending := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1}}}
	resolved := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 99}}}
	h.mempool.Add(MempoolEntry{Tx: pending})
	h.mempool.Add(MempoolEntry{Tx: resolved})
	h.completedResponses[1] = []byte("pending")
	h.completedResponses[99] = []byte("resolved")
	h.executing[1] = struct{}{}
	require.NoError(t, h.CatchUpForChallenge(context.Background(), f.p, 1, testPayload()))
	txs := h.mempool.Txs()
	require.Len(t, txs, 1)
	require.True(t, proto.Equal(pending, txs[0]))
	require.Contains(t, h.executing, uint64(1))
	require.Contains(t, h.completedResponses, uint64(1))
	require.NotContains(t, h.completedResponses, uint64(99))
}

func TestRefusalImportPreservesUncommittedFinish(t *testing.T) {
	f := newRefusalFixture(t)
	h := f.host(t, 1, storage.NewMemory())
	// Execution can finish before the gateway commits ConfirmStart. Both the
	// old local state and the newer certified state still say Pending.
	rec0, _ := h.sm.GetInference(1)
	receipt := testutil.SignExecutorReceipt(t, f.hosts[1], "escrow-1", 1, rec0.PromptHash, rec0.Model, rec0.InputLength, rec0.MaxTokens, rec0.StartedAt, 2000)
	h.mempool.Add(MempoolEntry{Tx: &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: receipt, ConfirmedAt: 2000}}}, ProposedAt: 1})
	finish := &types.MsgFinishInference{InferenceId: 1, EscrowId: "escrow-1", ExecutorSlot: 1, ResponseHash: []byte("completed"), InputTokens: 10, OutputTokens: 20}
	finish.ProposerSig = testutil.SignProposerTx(t, f.hosts[1], finish)
	h.mempool.Add(MempoolEntry{Tx: &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finish}}, ProposedAt: 1})
	require.Len(t, h.mempool.Txs(), 2)
	require.NoError(t, h.CatchUpForChallenge(context.Background(), f.p, 1, testPayload()))
	rec, ok := h.sm.GetInference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)
	require.Len(t, h.mempool.Txs(), 2, "snapshot has not committed this finish; recovery must retain outstanding work")
}

func TestRefusalShortenedCatchUp(t *testing.T) {
	f := newRefusalFixture(t)
	p := &types.RefusalPackage{EscrowID: f.p.EscrowID, Version: f.p.Version, N: 3, T: 4, BaseRoot: f.diffs[2].PostStateRoot, Diffs: f.diffs[3:4]}
	for _, n := range []uint64{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := &countingGetDiffsStore{Storage: storage.NewMemory()}
			h := f.host(t, n, s)
			err := h.CatchUpForChallenge(context.Background(), p, 1, testPayload())
			if n < p.N {
				require.ErrorIs(t, err, ErrChallengeStateChanged)
				require.Equal(t, n, h.LatestNonce())
			} else {
				require.NoError(t, err)
				require.Equal(t, max(n, p.T), h.LatestNonce())
			}
			require.Zero(t, s.gets.Load())
		})
	}
	h := f.host(t, 3, storage.NewMemory())
	_, err := h.sm.VerifyRefusalPackage(p)
	require.ErrorContains(t, err, "requires a snapshot")
	p.BaseRoot = make([]byte, 32)
	require.ErrorIs(t, h.CatchUpForChallenge(context.Background(), p, 1, testPayload()), ErrChallengeStateChanged)
	require.Equal(t, uint64(3), h.LatestNonce())
}
