package transport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/types"
)

func TestServeGetSignatures_HTTP(t *testing.T) {
	env := setupServerEnv(t)
	require.NoError(t, env.store.AppendDiff("escrow-1", types.DiffRecord{
		Diff: types.Diff{Nonce: 3},
	}))
	want := []byte{0xde, 0xad}
	require.NoError(t, env.store.AddSignature("escrow-1", 3, 0, want))

	got, err := env.server.ServeGetSignatures(3)
	require.NoError(t, err)
	require.Equal(t, want, got[0])

	rec := env.doGet(t, testRoutePrefix+"/sessions/escrow-1/signatures?nonce=3")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"0"`)
}

func TestServeGetDiffs_HTTP(t *testing.T) {
	env := setupServerEnv(t)
	d := types.Diff{Nonce: 1, UserSig: []byte{0x01}}
	require.NoError(t, env.store.AppendDiff("escrow-1", types.DiffRecord{
		Diff:      d,
		StateHash: []byte{0xaa},
	}))
	records, err := env.server.ServeGetDiffs(1, 1)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, uint64(1), records[0].Nonce)

	rec := env.doGet(t, testRoutePrefix+"/sessions/escrow-1/diffs?from=1&to=1")
	require.Equal(t, 200, rec.Code, rec.Body.String())
}

func TestServeGossipNonce_RejectsBadStateSig(t *testing.T) {
	env := setupServerEnv(t)
	require.ErrorIs(t, env.server.ServeGossipNonce(GossipNonceRequest{
		Nonce: 1, StateHash: []byte{1}, SlotID: 0,
	}), ErrGossipMissingStateSig)
	require.ErrorIs(t, env.server.ServeGossipNonce(GossipNonceRequest{
		Nonce: 1, StateHash: []byte{1}, StateSig: []byte{1, 2, 3}, SlotID: 0,
	}), ErrGossipInvalidStateSig)
	require.ErrorIs(t, env.server.ServeGossipNonce(GossipNonceRequest{
		Nonce: 1, StateHash: []byte{1}, StateSig: []byte{1}, SlotID: 99,
	}), ErrGossipInvalidSlot)
}

func TestServeHeightSyncRepair_InvalidRequesterSig(t *testing.T) {
	env := setupServerEnv(t)
	_, err := env.server.ServeHeightSyncRepair(context.Background(), env.hostSigner.Address(), &heightsync.RepairRequest{
		RequesterSlot: 0,
		RequesterSig:  []byte{0x01},
	})
	require.Error(t, err)
}
