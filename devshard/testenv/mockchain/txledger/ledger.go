package txledger

import (
	"encoding/hex"
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
}

// Ledger records broadcast transactions for GetTx queries.
type Ledger struct {
	mu  sync.RWMutex
	txs map[string]*StoredTx
}

// New returns an empty ledger.
func New() *Ledger {
	return &Ledger{txs: make(map[string]*StoredTx)}
}

// Put stores a successful tx by raw bytes.
func (l *Ledger) Put(txBytes []byte, events []txexec.Event) string {
	hash := Hash(txBytes)
	l.mu.Lock()
	l.txs[hash] = &StoredTx{Code: 0, Hash: hash, Events: events}
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

// Hash returns the uppercase hex tx hash for raw signed bytes.
func Hash(txBytes []byte) string {
	sum := tmhash.Sum(txBytes)
	return strings.ToUpper(hex.EncodeToString(sum))
}
