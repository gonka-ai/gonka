package host

import (
	"sync"
	"testing"

	"devshard/types"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestMempool_GossipCountBudgetPreservesAdmittedEntries(t *testing.T) {
	m := NewMempool()
	m.maxGossipTxs = 2
	first, second, third := finishTx(1), finishTx(2), finishTx(3)

	m.AddTx(first)
	m.AddTx(proto.Clone(first).(*types.DevshardTx))
	m.AddTx(second)
	m.AddTx(third)
	require.Equal(t, 2, m.Len(), "replays must not consume an additional slot")
	require.True(t, m.HasFinish(1), "overflow must not evict previously admitted recovery data")
	require.True(t, m.HasFinish(2))
	require.False(t, m.HasFinish(3))

	// Removal must release one slot even when the diff repeats a transaction
	// or also contains a transaction that was never admitted.
	m.RemoveIncluded([]*types.DevshardTx{first, first, finishTx(99)})
	m.AddTx(third)
	m.AddTx(finishTx(4))
	require.Equal(t, 2, m.Len())
	require.True(t, m.HasFinish(2))
	require.True(t, m.HasFinish(3))
	require.False(t, m.HasFinish(4))
}

func TestMempool_GossipByteBudget(t *testing.T) {
	m := NewMempool()
	m.maxGossipTxs = 10
	first, second, third := finishTx(1), finishTx(2), finishTx(3)
	m.maxGossipBytes = proto.Size(first) + proto.Size(second)

	oversized := finishTx(4)
	oversized.GetFinishInference().ProposerSig = make([]byte, m.maxGossipBytes+1)
	m.AddTx(oversized)
	require.Zero(t, m.Len(), "a single oversized transaction must not consume the budget")

	m.AddTx(first)
	m.AddTx(proto.Clone(first).(*types.DevshardTx))
	m.AddTx(second)
	require.Equal(t, 2, m.Len(), "transactions exactly filling the byte budget should be admitted")
	m.AddTx(third)
	require.False(t, m.HasFinish(3), "bytes must be bounded even below the entry limit")

	m.RemoveIncluded([]*types.DevshardTx{first, first})
	m.AddTx(third)
	m.AddTx(finishTx(5))
	require.Equal(t, 2, m.Len())
	require.True(t, m.HasFinish(2))
	require.True(t, m.HasFinish(3))
	require.False(t, m.HasFinish(5), "removal must not release the same bytes twice")
}

func TestMempool_FullGossipBudgetPreservesLocalAndVerifiedRecovery(t *testing.T) {
	m := NewMempool()
	gossiped := finishTx(1)
	m.maxGossipTxs = 1
	m.maxGossipBytes = proto.Size(gossiped)
	m.AddTx(gossiped)

	local := finishTx(2)
	local.GetFinishInference().ProposerSig = make([]byte, m.maxGossipBytes+1)
	m.Add(MempoolEntry{Tx: local, ProposedAt: 5})
	recovered := finishTx(3)
	recovered.GetFinishInference().ProposerSig = make([]byte, m.maxGossipBytes+1)
	m.AddRecoveryTx(recovered)
	// A local entry is protected by its admission path, even at nonce zero.
	m.Add(MempoolEntry{Tx: finishTx(4), ProposedAt: 0})
	m.AddTx(finishTx(5))
	require.Equal(t, 4, m.Len())
	for _, id := range []uint64{1, 2, 3, 4} {
		require.True(t, m.HasFinish(id))
	}
	require.False(t, m.HasFinish(5))

	m.RemoveIncluded([]*types.DevshardTx{local, recovered, finishTx(4)})
	m.AddTx(finishTx(5))
	require.False(t, m.HasFinish(5), "removing protected work must not free an occupied gossip slot")
	m.RemoveIncluded([]*types.DevshardTx{gossiped})
	m.AddTx(finishTx(5))
	require.Equal(t, 1, m.Len())
	require.True(t, m.HasFinish(5))
}

func TestMempool_GossipPromotionReleasesBudgetOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		promote func(*Mempool, *types.DevshardTx)
	}{
		{
			name: "local",
			promote: func(m *Mempool, tx *types.DevshardTx) {
				m.Add(MempoolEntry{Tx: tx, ProposedAt: 5})
			},
		},
		{
			name: "verified recovery",
			promote: func(m *Mempool, tx *types.DevshardTx) {
				m.AddRecoveryTx(tx)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMempool()
			first := finishTx(1)
			m.maxGossipTxs = 1
			m.maxGossipBytes = proto.Size(first)
			m.AddTx(first)
			tc.promote(m, proto.Clone(first).(*types.DevshardTx))
			m.AddTx(proto.Clone(first).(*types.DevshardTx))
			m.AddTx(finishTx(2))
			require.Equal(t, 2, m.Len(), "promotion must free both gossip budgets; replay must not downgrade it")
			require.True(t, m.HasFinish(1))
			require.True(t, m.HasFinish(2))

			tc.promote(m, proto.Clone(first).(*types.DevshardTx))
			m.AddTx(finishTx(3))
			require.False(t, m.HasFinish(3), "repeated promotion must not release the budget twice")
			m.RemoveIncluded([]*types.DevshardTx{first})
			m.AddTx(finishTx(3))
			require.False(t, m.HasFinish(3), "removing the promoted entry must not release another gossip slot")
		})
	}
}

func TestMempool_PeerReplayPreservesLocalRecoveryMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		replay func(*Mempool, *types.DevshardTx)
	}{
		{"gossip", func(m *Mempool, tx *types.DevshardTx) { m.AddTx(tx) }},
		{"verified recovery", func(m *Mempool, tx *types.DevshardTx) { m.AddRecoveryTx(tx) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMempool()
			finish, confirm := finishTx(1), confirmTx(1, 42)
			m.Add(MempoolEntry{Tx: finish, ProposedAt: 5})
			m.Add(MempoolEntry{Tx: confirm, ProposedAt: 5})
			tc.replay(m, proto.Clone(finish).(*types.DevshardTx))
			tc.replay(m, proto.Clone(confirm).(*types.DevshardTx))

			require.Empty(t, m.StaleFinishes(7, 2))
			stale := m.StaleFinishes(8, 2)
			require.Len(t, stale, 1, "the local Finish must remain eligible for recovery rebroadcast")
			require.True(t, proto.Equal(finish, stale[0]))
			receipt := m.QueuedConfirmStart(1)
			require.NotNil(t, receipt, "a peer replay must not erase the host's reusable receipt")
			require.True(t, proto.Equal(confirm.GetConfirmStart(), receipt))
		})
	}
}

func TestMempool_LocalReplacementKeepsProposalNonceSemantics(t *testing.T) {
	m := NewMempool()
	m.Add(MempoolEntry{Tx: finishTx(1), ProposedAt: 5})
	m.Add(MempoolEntry{Tx: finishTx(1), ProposedAt: 6})
	require.Equal(t, 1, m.Len())
	require.Empty(t, m.StaleFinishes(8, 2), "a newer local proposal still replaces the old proposal nonce")
	require.Len(t, m.StaleFinishes(9, 2), 1)
}

func TestMempool_ConcurrentGossipAdmissionRespectsBudgets(t *testing.T) {
	const capacity = 32
	for _, tc := range []struct {
		name  string
		setup func(*Mempool)
	}{
		{"count", func(m *Mempool) { m.maxGossipTxs = capacity }},
		{"bytes", func(m *Mempool) { m.maxGossipBytes = capacity * proto.Size(finishTx(1)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMempool()
			tc.setup(m)
			for wave := 0; wave < 2; wave++ {
				var wg sync.WaitGroup
				start := make(chan struct{})
				// IDs below 128 have equal encoded size, so either budget admits
				// exactly capacity entries regardless of goroutine ordering.
				for id := uint64(1); id <= 96; id++ {
					wg.Add(1)
					go func(tx *types.DevshardTx) {
						defer wg.Done()
						<-start
						m.AddTx(tx)
						m.AddTx(tx)
					}(finishTx(id))
				}
				close(start)
				wg.Wait()
				require.Equal(t, capacity, m.Len())
				m.RemoveIncluded(m.Txs())
				require.Zero(t, m.Len())
			}
		})
	}
}
