package host

import (
	"cmp"
	"slices"
	"sync"

	"devshard/types"
)

// mempoolGlobalInference sorts session-wide txs after all per-inference txs.
const mempoolGlobalInference = ^uint64(0)

// mempoolInferencePhase returns (inference_id, phase) for deterministic mempool ordering.
// Lower phase runs first for the same inference (ConfirmStart before FinishInference).
func mempoolInferencePhase(tx *types.DevshardTx) (uint64, uint8) {
	if si := tx.GetStartInference(); si != nil {
		return si.InferenceId, 0
	}
	if cs := tx.GetConfirmStart(); cs != nil {
		return cs.InferenceId, 1
	}
	if fi := tx.GetFinishInference(); fi != nil {
		return fi.InferenceId, 2
	}
	if ti := tx.GetTimeoutInference(); ti != nil {
		return ti.InferenceId, 3
	}
	if v := tx.GetValidation(); v != nil {
		return v.InferenceId, 4
	}
	if vv := tx.GetValidationVote(); vv != nil {
		return vv.InferenceId, 5
	}
	if tx.GetRevealSeed() != nil {
		return mempoolGlobalInference, 10
	}
	if tx.GetFinalizeRound() != nil {
		return mempoolGlobalInference, 11
	}
	if ft := tx.GetForceHeightSyncTurn(); ft != nil {
		// Stable tie-break for rare mempool presence.
		return mempoolGlobalInference, 12
	}
	return mempoolGlobalInference, 255
}

func mempoolTxLess(a, b *types.DevshardTx) int {
	ai, ap := mempoolInferencePhase(a)
	bi, bp := mempoolInferencePhase(b)
	if c := cmp.Compare(ai, bi); c != 0 {
		return c
	}
	if c := cmp.Compare(ap, bp); c != 0 {
		return c
	}
	return cmp.Compare(types.TxHash(a), types.TxHash(b))
}

// MempoolEntry tracks a host-proposed tx awaiting inclusion.
type MempoolEntry struct {
	Tx         *types.DevshardTx
	ProposedAt uint64 // nonce when proposed
}

// Mempool stores host-proposed txs that haven't been included in a diff yet.
// Keyed by txHash for O(1) lookup and O(m) removal.
type Mempool struct {
	mu      sync.Mutex
	entries map[uint64]MempoolEntry
}

func NewMempool() *Mempool {
	return &Mempool{entries: make(map[uint64]MempoolEntry)}
}

func (m *Mempool) Add(entry MempoolEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[types.TxHash(entry.Tx)] = entry
}

// RemoveIncluded removes entries whose tx matches any tx in the diff (by hash).
func (m *Mempool) RemoveIncluded(txs []*types.DevshardTx) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, tx := range txs {
		delete(m.entries, types.TxHash(tx))
	}
}

// HasStaleEntry returns true if any entry was proposed more than grace nonces ago.
// This is a pure data query with no signing decision.
func (m *Mempool) HasStaleEntry(currentNonce, grace uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.ProposedAt+grace < currentNonce {
			return true
		}
	}
	return false
}

func (m *Mempool) Txs() []*types.DevshardTx {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return nil
	}
	txs := make([]*types.DevshardTx, 0, len(m.entries))
	for _, e := range m.entries {
		txs = append(txs, e.Tx)
	}
	slices.SortFunc(txs, mempoolTxLess)
	return txs
}

func (m *Mempool) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// AddTx wraps Add with a zero ProposedAt. Satisfies gossip.MempoolSink.
func (m *Mempool) AddTx(tx *types.DevshardTx) {
	m.Add(MempoolEntry{Tx: tx, ProposedAt: 0})
}


