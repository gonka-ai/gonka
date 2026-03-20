package host

import (
	"sync"

	"subnet/types"
)

// MempoolEntry tracks a host-proposed tx awaiting inclusion.
type MempoolEntry struct {
	Tx         *types.SubnetTx
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
func (m *Mempool) RemoveIncluded(txs []*types.SubnetTx) {
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

func (m *Mempool) Txs() []*types.SubnetTx {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return nil
	}
	txs := make([]*types.SubnetTx, 0, len(m.entries))
	for _, e := range m.entries {
		txs = append(txs, e.Tx)
	}
	return txs
}

func (m *Mempool) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// AddTx wraps Add with a zero ProposedAt. Satisfies gossip.MempoolSink.
func (m *Mempool) AddTx(tx *types.SubnetTx) {
	m.Add(MempoolEntry{Tx: tx, ProposedAt: 0})
}


