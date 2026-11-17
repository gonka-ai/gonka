package txledger

import (
	"encoding/hex"
	"sort"
	"strings"
	"sync"

	"github.com/cometbft/cometbft/crypto/tmhash"

	"devshard/testenv/mockchain/txexec"
)

// StoredTx is a committed mock-chain transaction.
type StoredTx struct {
	Code   uint32
	Hash   string
	Events []txexec.Event
	// Raw is the signed TxRaw as broadcast; Seq is the commit order.
	Raw []byte
	Seq uint64
}

// Ledger records broadcast transactions for GetTx queries.
type Ledger struct {
	mu  sync.RWMutex
	txs map[string]*StoredTx
	seq uint64
}

// New returns an empty ledger.
func New() *Ledger {
	return &Ledger{txs: make(map[string]*StoredTx)}
}

// Put stores a successful tx by raw bytes.
func (l *Ledger) Put(txBytes []byte, events []txexec.Event) string {
	hash := Hash(txBytes)
	l.mu.Lock()
	l.seq++
	l.txs[hash] = &StoredTx{Code: 0, Hash: hash, Events: events, Raw: append([]byte(nil), txBytes...), Seq: l.seq}
	l.mu.Unlock()
	return hash
}

// Get returns a stored tx by hash (case-insensitive).
func (l *Ledger) Get(hash string) (*StoredTx, bool) {
	hash = strings.ToUpper(strings.TrimSpace(hash))
	l.mu.RLock()
	tx, ok := l.txs[hash]
	l.mu.RUnlock()
	return tx, ok
}

// List returns every stored tx in commit order. Identical bytes broadcast
// twice share one hash, so they appear once, at the latest position.
func (l *Ledger) List() []StoredTx {
	l.mu.RLock()
	out := make([]StoredTx, 0, len(l.txs))
	for _, tx := range l.txs {
		out = append(out, *tx)
	}
	l.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Hash returns the uppercase hex tx hash for raw signed bytes.
func Hash(txBytes []byte) string {
	sum := tmhash.Sum(txBytes)
	return strings.ToUpper(hex.EncodeToString(sum))
}
