package host

import (
	"sync"

	"devshard/types"
)

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

// StaleFinishes returns locally-proposed MsgFinishInference txs (ProposedAt > 0)
// that have been sitting in the mempool for more than `grace` nonces past
// their proposal point (e.ProposedAt + grace < currentNonce).
//
// RemoveIncluded clears any entry whose tx hash is in an applied diff, so an
// entry still present after applyAndPersist at nonce N was, by definition,
// not included in any diff up to and including N. The `grace` adds a buffer
// on top of that: callers typically set it to two full slot rotations
// (2 * len(group)) so the user has at least two chances to pick up the
// Finish via natural round-robin contact with the executor host before we
// resort to peer-to-peer recovery gossip.
//
// Used by the host to recover from "user as sole sequencer" stalls: if the
// user diff failed to absorb our Finish (e.g. because the OpenAI client at
// devshardctl disconnected before reading devshard_meta, or the proxy
// crashed mid-drain), we re-broadcast it via tx gossip so peers can learn
// about it and -- crucially -- include it in their own mempool returns to
// the user, giving the user another path to sequence it into a signed diff.
//
// Peer-imported entries (ProposedAt == 0, set by AddTx via gossip.OnTxsReceived)
// are intentionally excluded so we don't amplify other hosts' broadcasts.
//
// Iteration order is unspecified; consumers (gossip broadcast dedups by tx
// hash, user re-sorts pending txs) are order-insensitive.
func (m *Mempool) StaleFinishes(currentNonce, grace uint64) []*types.DevshardTx {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return nil
	}
	var out []*types.DevshardTx
	for _, e := range m.entries {
		if e.ProposedAt == 0 {
			continue
		}
		if e.Tx.GetFinishInference() == nil {
			continue
		}
		if e.ProposedAt+grace < currentNonce {
			out = append(out, e.Tx)
		}
	}
	return out
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
