package host

import (
	"bytes"
	"sync"

	"devshard/types"
	"google.golang.org/protobuf/proto"
)

// These are per-host-session budgets for retained peer-gossip transactions,
// independent of transport message limits. The byte budget measures protobuf
// encoding size, not Go heap usage; the count budget also bounds entry overhead.
const (
	MaxGossipMempoolTxs   = 4096
	MaxGossipMempoolBytes = 8 << 20
)

// MempoolEntry tracks a host-proposed tx awaiting inclusion.
type MempoolEntry struct {
	Tx         *types.DevshardTx
	ProposedAt uint64 // nonce when proposed
}

type mempoolSource uint8

const (
	mempoolGossip mempoolSource = iota
	mempoolRecovery
	mempoolLocal
)

type retainedTx struct {
	MempoolEntry
	source       mempoolSource
	encodedBytes int // charged only for gossip entries
}

// Mempool stores host-proposed txs that haven't been included in a diff yet.
// Keyed by txHash for O(1) lookup and O(m) removal.
type Mempool struct {
	mu      sync.Mutex
	entries map[uint64]retainedTx

	gossipCount    int
	gossipBytes    int
	maxGossipTxs   int
	maxGossipBytes int
}

func NewMempool() *Mempool {
	return &Mempool{
		entries:        make(map[uint64]retainedTx),
		maxGossipTxs:   MaxGossipMempoolTxs,
		maxGossipBytes: MaxGossipMempoolBytes,
	}
}

// Add retains locally proposed work, even when the gossip budget is full.
func (m *Mempool) Add(entry MempoolEntry) {
	m.addProtected(entry, mempoolLocal)
}

// AddRecoveryTx retains recovery work already verified by the caller. It must
// not compete with unsolicited gossip for admission, nor overwrite the local
// proposal nonce of an identical transaction.
func (m *Mempool) AddRecoveryTx(tx *types.DevshardTx) {
	m.addProtected(MempoolEntry{Tx: tx}, mempoolRecovery)
}

func (m *Mempool) addProtected(entry MempoolEntry, source mempoolSource) {
	if entry.Tx == nil {
		return
	}
	hash := types.TxHash(entry.Tx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.entries[hash]; ok {
		if previous.source > source {
			return
		}
		m.releaseGossip(previous)
	}
	m.entries[hash] = retainedTx{MempoolEntry: entry, source: source}
}

// releaseGossip releases the admission budget before removal or promotion.
// Caller holds m.mu.
func (m *Mempool) releaseGossip(entry retainedTx) {
	if entry.source == mempoolGossip {
		m.gossipCount--
		m.gossipBytes -= entry.encodedBytes
	}
}

// RemoveIncluded removes entries whose tx matches any tx in the diff (by hash).
func (m *Mempool) RemoveIncluded(txs []*types.DevshardTx) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, tx := range txs {
		hash := types.TxHash(tx)
		if entry, ok := m.entries[hash]; ok {
			m.releaseGossip(entry)
			delete(m.entries, hash)
		}
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

// HasFinish reports whether a MsgFinishInference for inferenceID is waiting
// in the mempool.
func (m *Mempool) HasFinish(inferenceID uint64) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.Tx == nil {
			continue
		}
		if fi := e.Tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			return true
		}
	}
	return false
}

// QueuedConfirmStart returns the MsgConfirmStart this host proposed for
// inferenceID, or nil. Peer-imported entries (ProposedAt == 0) are skipped:
// AddTx does not verify signatures, so a gossiped copy must not stand in for
// this host's own receipt. With several queued, the earliest proposed wins.
func (m *Mempool) QueuedConfirmStart(inferenceID uint64) *types.MsgConfirmStart {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out *types.MsgConfirmStart
	var outAt uint64
	for _, e := range m.entries {
		if e.ProposedAt == 0 || e.Tx == nil {
			continue
		}
		cs := e.Tx.GetConfirmStart()
		if cs == nil || cs.InferenceId != inferenceID {
			continue
		}
		if out == nil || e.ProposedAt < outAt || e.ProposedAt == outAt && bytes.Compare(cs.ExecutorSig, out.ExecutorSig) < 0 {
			out, outAt = cs, e.ProposedAt
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

// AddTx admits best-effort peer gossip within both cumulative budgets. At
// capacity, new entries are dropped; already retained work is not evicted.
// Replays neither consume budget nor replace a local/recovery entry. Local
// proposals and verified recovery must use Add and AddRecoveryTx instead.
// Satisfies gossip.MempoolSink; gossip acknowledgement is not an admission receipt.
func (m *Mempool) AddTx(tx *types.DevshardTx) {
	if tx == nil {
		return
	}
	size := proto.Size(tx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if size > m.maxGossipBytes {
		return
	}
	hash := types.TxHash(tx)
	if _, ok := m.entries[hash]; ok {
		return
	}
	if m.gossipCount >= m.maxGossipTxs || size > m.maxGossipBytes-m.gossipBytes {
		return
	}
	m.entries[hash] = retainedTx{
		MempoolEntry: MempoolEntry{Tx: tx},
		source:       mempoolGossip,
		encodedBytes: size,
	}
	m.gossipCount++
	m.gossipBytes += size
}
