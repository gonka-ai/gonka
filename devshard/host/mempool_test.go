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
	buildMixedMempool := func() *Mempool {
		m := NewMempool()
		m.Add(MempoolEntry{Tx: finishTx(1), ProposedAt: 5}) // local finish
		m.Add(MempoolEntry{Tx: validationTx(1, 0), ProposedAt: 5})
		m.AddTx(finishTx(2)) // peer-imported finish (ProposedAt=0 sentinel)
		return m
	}
	staleFinishIDs := func(txs []*types.DevshardTx) []uint64 {
		var ids []uint64
		for _, tx := range txs {
			fi := tx.GetFinishInference()
			if fi != nil {
				ids = append(ids, fi.InferenceId)
			}
		}
		return ids
	}

	tests := []struct {
		name         string
		build        func() *Mempool
		currentNonce uint64
		grace        uint64
		wantNil      bool
		wantFinishID []uint64
	}{
		{
			name:         "empty mempool returns nil",
			build:        func() *Mempool { return NewMempool() },
			currentNonce: 10,
			grace:        0,
			wantNil:      true,
		},
		{
			name:         "at proposed nonce not stale",
			build:        buildMixedMempool,
			currentNonce: 5,
			grace:        0,
			wantFinishID: nil,
		},
		{
			name:         "past proposed nonce stale local finish only",
			build:        buildMixedMempool,
			currentNonce: 6,
			grace:        0,
			wantFinishID: []uint64{1},
		},
		{
			name:         "grace boundary not exceeded",
			build:        buildMixedMempool,
			currentNonce: 7,
			grace:        2,
			wantFinishID: nil,
		},
		{
			name:         "grace exceeded marks stale",
			build:        buildMixedMempool,
			currentNonce: 8,
			grace:        2,
			wantFinishID: []uint64{1},
		},
		{
			name:         "non-finish tx types never returned",
			build:        buildMixedMempool,
			currentNonce: 99,
			grace:        0,
			wantFinishID: []uint64{1},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m := tc.build()
			got := m.StaleFinishes(tc.currentNonce, tc.grace)
			if tc.wantNil {
				require.Nil(t, got)
				return
			}
			require.Equal(t, tc.wantFinishID, staleFinishIDs(got))
		})
	}

	t.Run("peer-imported finish is never returned across nonce range", func(t *testing.T) {
		m := buildMixedMempool()
		for n := uint64(1); n < 100; n++ {
			for _, tx := range m.StaleFinishes(n, 0) {
				require.NotEqual(t, uint64(2), tx.GetFinishInference().InferenceId,
					"peer-imported Finish must be excluded")
			}
		}
	})
}
