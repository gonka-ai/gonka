package transport_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcserver"
	"devshard/types"
)

type catchUpRPCCore struct {
	snapshotHits *atomic.Int32
	diffHits     *atomic.Int32
	snap         *host.PeerSnapshot
	diffs        []types.Diff
}

func (c catchUpRPCCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return map[uint32][]byte{}, nil
}
func (c catchUpRPCCore) AllowsSender(string) bool  { return true }
func (c catchUpRPCCore) IsGroupMember(string) bool { return true }

func (c catchUpRPCCore) ServeGetSnapshot(maxNonce uint64) (*host.PeerSnapshot, error) {
	if c.snapshotHits != nil {
		c.snapshotHits.Add(1)
	}
	if c.snap == nil || (maxNonce > 0 && c.snap.Nonce > maxNonce) {
		return nil, storage.ErrSnapshotNotFound
	}
	return c.snap, nil
}

func (c catchUpRPCCore) ServeGetDiffs(fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	if c.diffHits != nil {
		c.diffHits.Add(1)
	}
	if toNonce-fromNonce >= storage.DiffPageMaxNonces {
		return nil, storage.ErrDiffPageLimit
	}
	var out []types.DiffRecord
	for _, d := range c.diffs {
		if d.Nonce >= fromNonce && d.Nonce <= toNonce {
			out = append(out, types.DiffRecord{Diff: d, StateHash: d.PostStateRoot})
		}
	}
	return out, nil
}

type catchUpRPCLookup struct {
	core catchUpRPCCore
}

func (l catchUpRPCLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return l.core, nil
}
func (l catchUpRPCLookup) SessionForParticipant(id, _ string) (rpcserver.SessionCore, error) {
	return l.SessionServerExisting(id)
}
func (l catchUpRPCLookup) SessionForOwner(id, _ string) (rpcserver.SessionCore, error) {
	return l.SessionServerExisting(id)
}
func (l catchUpRPCLookup) SessionForStartProof(id, addr string, _ func() ([]types.Diff, error), _ string) (rpcserver.SessionCore, error) {
	return l.SessionForParticipant(id, addr)
}

func TestRPC_GetSnapshotAndGetDiffPages_RoundTrip(t *testing.T) {
	userSigner := testutil.MustGenerateKey(t)
	diffs := make([]types.Diff, 70)
	for i := range diffs {
		n := uint64(i + 1)
		diffs[i] = testutil.SignDiffWithRoot(t, userSigner, "escrow-1", n, []*types.DevshardTx{testutil.StartTx(n)}, []byte("0123456789abcdef0123456789abcdef"))
	}
	anchor := diffs[64]
	floorRef := diffs[31]

	var snapHits, diffHits atomic.Int32
	core := catchUpRPCCore{
		snapshotHits: &snapHits,
		diffHits:     &diffHits,
		snap: &host.PeerSnapshot{
			Nonce:        65,
			Snapshot:     []byte("proto-snapshot-bytes"),
			AnchorDiff:   &anchor,
			FloorRefDiff: &floorRef,
			Signatures:   map[uint32][]byte{1: []byte("sig-1"), 2: []byte("sig-2")},
		},
		diffs: diffs,
	}

	rpc := newLargeRPCClient(t, catchUpRPCLookup{core: core}, transport.EndpointDiffs, transport.DefaultClientConfig())
	t.Cleanup(rpc.Close)

	ctx := context.Background()

	// 1. maxNonce below snapshot nonce maps CodeNotFound -> storage.ErrSnapshotNotFound.
	_, err := rpc.GetSnapshot(ctx, 50)
	require.ErrorIs(t, err, storage.ErrSnapshotNotFound)

	// 2. GetSnapshot over Connect-RPC round-trips snapshot, anchor diff, floor ref diff, and sigs.
	got, err := rpc.GetSnapshot(ctx, 70)
	require.NoError(t, err)
	require.Equal(t, uint64(65), got.Nonce)
	require.Equal(t, []byte("proto-snapshot-bytes"), got.Snapshot)
	require.NotNil(t, got.AnchorDiff)
	require.Equal(t, uint64(65), got.AnchorDiff.Nonce)
	require.NotNil(t, got.FloorRefDiff)
	require.Equal(t, uint64(32), got.FloorRefDiff.Nonce)
	require.Equal(t, []byte("sig-1"), got.Signatures[1])
	require.Equal(t, int32(2), snapHits.Load())

	// 3. GetDiffPages walks > 64 nonces across 2 bounded Connect-RPC GetDiffs pages.
	var collected []uint64
	err = rpc.GetDiffPages(ctx, 1, 70, func(page []types.Diff) error {
		for _, d := range page {
			collected = append(collected, d.Nonce)
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, collected, 70)
	require.Equal(t, uint64(1), collected[0])
	require.Equal(t, uint64(70), collected[69])
	require.Equal(t, int32(2), diffHits.Load(), "70 nonces must split into 2 RPC pages (64 + 6)")
}
