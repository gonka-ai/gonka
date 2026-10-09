package host

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/types"
)

func TestRefusalRejectsKnownFork(t *testing.T) {
	f := newRefusalFixture(t)
	snap := *f.states[1]
	snap.SealedAcc = make([]byte, 32)
	data, err := types.MarshalStateSnapshotProto(&snap, nil, nil)
	require.NoError(t, err)
	p := &types.RefusalPackage{EscrowID: "escrow-1", Version: snap.StateRootAndProtocolVersion, N: 1, T: 4, Snapshot: data, Diffs: []types.Diff{f.diffs[1]}, Signatures: map[uint64]map[uint32][]byte{2: {}}}
	content, err := proto.Marshal(&types.StateSignatureContent{EscrowId: p.EscrowID, Nonce: 2, StateRoot: f.diffs[1].PostStateRoot})
	require.NoError(t, err)
	for slot, signer := range f.hosts {
		p.Signatures[2][uint32(slot)], err = signer.Sign(content)
		require.NoError(t, err)
	}
	fork := f.host(t, 2, storage.NewMemory())
	for n := uint64(3); n <= 4; n++ {
		var txs []*types.DevshardTx
		if n == 3 {
			txs = []*types.DevshardTx{testutil.StartTx(3)}
		}
		root, err := fork.sm.ApplyLocal(n, txs)
		require.NoError(t, err)
		p.Diffs = append(p.Diffs, testutil.SignDiffWithRoot(t, f.user, p.EscrowID, n, txs, root))
	}
	for _, n := range []uint64{3, 4, 5} {
		verifier := f.host(t, n, storage.NewMemory())
		require.ErrorIs(t, verifier.VerifyRefusalPackage(p, 1, testPayload()), types.ErrStateHashMismatch)
		require.Equal(t, n, verifier.LatestNonce())
	}
	executor := f.host(t, 3, storage.NewMemory())
	challengeErr := executor.CatchUpForChallenge(context.Background(), p, 1, testPayload())
	require.ErrorIs(t, challengeErr, types.ErrPostStateRootMismatch)
	// The same executor accepts the verifier's own stored history.
	receipt, _, err := executor.ChallengeReceipt(context.Background(), 1, testPayload(), f.diffs[:4])
	require.NoError(t, err)
	require.NotEmpty(t, receipt)
	// Matching an earlier verifier head would not prevent this fork.
	behind := f.host(t, 2, storage.NewMemory())
	_, root, err := behind.StateHead()
	require.NoError(t, err)
	require.Equal(t, root, p.Diffs[0].PostStateRoot)
	require.NoError(t, behind.VerifyRefusalPackage(p, 1, testPayload()))
}

func TestRefusalKnownStatePositions(t *testing.T) {
	f := newRefusalFixture(t)
	for _, n := range []uint64{1, 2, 3, 4, 5} {
		h := f.host(t, n, storage.NewMemory())
		require.NoError(t, h.VerifyRefusalPackage(f.p, 1, testPayload()))
		if n >= f.p.N && n <= f.p.T {
			st := h.sm.ExportState()
			st.Balance++
			h.sm.RestoreState(st)
			require.ErrorIs(t, h.VerifyRefusalPackage(f.p, 1, testPayload()), types.ErrStateHashMismatch)
		}
	}
	// A snapshot-only package must also agree with the local state.
	p := *f.p
	p.T = p.N
	p.Diffs = nil
	p.Signatures = map[uint64]map[uint32][]byte{p.N: {}}
	content, err := proto.Marshal(&types.StateSignatureContent{EscrowId: p.EscrowID, Nonce: p.N, StateRoot: f.diffs[p.N-1].PostStateRoot})
	require.NoError(t, err)
	for slot, signer := range f.hosts {
		p.Signatures[p.N][uint32(slot)], err = signer.Sign(content)
		require.NoError(t, err)
	}
	h := f.host(t, p.N, storage.NewMemory())
	require.NoError(t, h.VerifyRefusalPackage(&p, 1, testPayload()))
	st := h.sm.ExportState()
	st.Balance++
	h.sm.RestoreState(st)
	require.ErrorIs(t, h.VerifyRefusalPackage(&p, 1, testPayload()), types.ErrStateHashMismatch)
}

type refusalHistoryStore struct {
	storage.Storage
	err error
}

func (s *refusalHistoryStore) GetDiffs(_ string, from, to uint64) ([]types.DiffRecord, error) {
	if from != 4 || to != 4 {
		return nil, fmt.Errorf("unexpected history range %d..%d", from, to)
	}
	return nil, s.err
}

func TestRefusalUnavailableHistory(t *testing.T) {
	f := newRefusalFixture(t)
	s := &refusalHistoryStore{Storage: storage.NewMemory()}
	h := f.host(t, 1, s)
	require.NoError(t, h.ImportVerifiedSnapshot(f.states[5]))
	// The imported snapshot did not retain the root at T=4.
	require.NoError(t, h.VerifyRefusalPackage(f.p, 1, testPayload()))
	s.err = errors.New("storage unavailable")
	require.ErrorIs(t, h.VerifyRefusalPackage(f.p, 1, testPayload()), s.err)
}
