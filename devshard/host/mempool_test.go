package host

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func finishTx(inferenceID uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{InferenceId: inferenceID},
	}}
}

func TestMempool_AddAndTxs(t *testing.T) {
	m := NewMempool()
	require.Equal(t, 0, m.Len())
	require.Nil(t, m.Txs())

	tx1 := finishTx(1)
	tx2 := finishTx(2)
	m.Add(MempoolEntry{Tx: tx1, ProposedAt: 5})
	m.Add(MempoolEntry{Tx: tx2, ProposedAt: 6})

	require.Equal(t, 2, m.Len())
	txs := m.Txs()
	require.Len(t, txs, 2)
}

func validationTx(inferenceID uint64, slot uint32) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Validation{
		Validation: &types.MsgValidation{InferenceId: inferenceID, ValidatorSlot: slot},
	}}
}

func TestMempool_RemoveIncluded(t *testing.T) {
	m := NewMempool()
	m.Add(MempoolEntry{Tx: validationTx(1, 0), ProposedAt: 5})
	m.Add(MempoolEntry{Tx: validationTx(2, 1), ProposedAt: 6})
	m.Add(MempoolEntry{Tx: finishTx(3), ProposedAt: 7})

	// Remove validation for inference 2 only.
	m.RemoveIncluded([]*types.DevshardTx{validationTx(2, 1)})

	require.Equal(t, 2, m.Len())
}

func TestMempool_HasStaleEntry(t *testing.T) {
	m := NewMempool()
	m.Add(MempoolEntry{Tx: finishTx(1), ProposedAt: 5})

	// grace=3, currentNonce=8: 5+3=8, not < 8 -> not stale
	require.False(t, m.HasStaleEntry(8, 3))

	// grace=3, currentNonce=9: 5+3=8 < 9 -> stale
	require.True(t, m.HasStaleEntry(9, 3))
}

func TestMempool_RemoveOnlyMatching(t *testing.T) {
	m := NewMempool()
	m.Add(MempoolEntry{Tx: finishTx(1), ProposedAt: 5})
	m.Add(MempoolEntry{Tx: validationTx(1, 0), ProposedAt: 6})

	// Remove with a tx that doesn't match any entry.
	m.RemoveIncluded([]*types.DevshardTx{finishTx(99)})
	require.Equal(t, 2, m.Len())

	// Same inference_id but different tx type -- must not remove the validation.
	m.RemoveIncluded([]*types.DevshardTx{finishTx(1)})
	require.Equal(t, 1, m.Len())
	require.NotNil(t, m.Txs()[0].GetValidation())
}

func TestMempool_DuplicateAdd(t *testing.T) {
	m := NewMempool()
	m.Add(MempoolEntry{Tx: finishTx(1), ProposedAt: 5})
	m.Add(MempoolEntry{Tx: finishTx(1), ProposedAt: 6}) // same tx, overwrites

	require.Equal(t, 1, m.Len(), "duplicate tx should overwrite, not double-add")
}

func TestMempool_StaleFinishes(t *testing.T) {
	m := NewMempool()
	require.Nil(t, m.StaleFinishes(10, 0), "empty mempool returns nil")

	localFinish := finishTx(1)
	peerFinish := finishTx(2)
	val := validationTx(1, 0)
	m.Add(MempoolEntry{Tx: localFinish, ProposedAt: 5})
	m.Add(MempoolEntry{Tx: val, ProposedAt: 5})
	m.AddTx(peerFinish) // ProposedAt: 0 -- gossip-imported sentinel.

	// grace=0, currentNonce == ProposedAt: not yet stale.
	require.Empty(t, m.StaleFinishes(5, 0))

	// grace=0, currentNonce > ProposedAt: locally-proposed Finish is stale.
	stale := m.StaleFinishes(6, 0)
	require.Len(t, stale, 1, "only the local Finish should be returned")
	require.NotNil(t, stale[0].GetFinishInference())
	require.Equal(t, uint64(1), stale[0].GetFinishInference().InferenceId)

	// grace > 0 buffers the trigger: at ProposedAt+grace we are still not
	// stale (5+2 < 7 is false); only past that point.
	require.Empty(t, m.StaleFinishes(7, 2), "ProposedAt+grace not yet exceeded")
	require.Len(t, m.StaleFinishes(8, 2), 1, "exceeding ProposedAt+grace marks stale")

	// Peer-imported Finish (ProposedAt=0) must never be reported, regardless
	// of currentNonce or grace, to avoid amplifying other hosts' broadcasts.
	for n := uint64(1); n < 100; n++ {
		for _, tx := range m.StaleFinishes(n, 0) {
			require.NotEqual(t, uint64(2), tx.GetFinishInference().InferenceId,
				"peer-imported Finish must be excluded")
		}
	}

	// Validations and other non-Finish tx types are never returned, even when
	// locally proposed and past their proposal nonce.
	require.Len(t, m.StaleFinishes(99, 0), 1, "only Finish txs are eligible")
}
