package user

import (
	"bytes"
	"maps"
	"sync"

	"devshard/logging"
	"devshard/storage"
)

// sigWriter persists state signatures in arrival order off Session.mu (#1986);
// unwritten entries stay readable through pendingAt.
type sigWriter struct {
	store    storage.Storage
	escrowID string

	mu      sync.Mutex
	idle    *sync.Cond
	queue   []sigWrite
	pending map[uint64]map[uint32][]byte
	running bool
}

type sigWrite struct {
	nonce uint64
	slot  uint32
	sig   []byte
}

func newSigWriter(store storage.Storage, escrowID string) *sigWriter {
	w := &sigWriter{store: store, escrowID: escrowID, pending: make(map[uint64]map[uint32][]byte)}
	w.idle = sync.NewCond(&w.mu)
	return w
}

func (w *sigWriter) enqueue(nonce uint64, slot uint32, sig []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.queue = append(w.queue, sigWrite{nonce: nonce, slot: slot, sig: sig})
	if w.pending[nonce] == nil {
		w.pending[nonce] = make(map[uint32][]byte)
	}
	w.pending[nonce][slot] = sig
	if !w.running {
		w.running = true
		go w.run()
	}
}

func (w *sigWriter) run() {
	w.mu.Lock()
	for len(w.queue) > 0 {
		batch := w.queue
		w.queue = nil
		w.mu.Unlock()
		for _, e := range batch {
			if err := w.store.AddSignature(w.escrowID, e.nonce, e.slot, e.sig); err != nil {
				logging.Warn("failed to persist signature",
					"escrow_id", w.escrowID, "nonce", e.nonce, "slot", e.slot, "error", err)
			}
		}
		w.mu.Lock()
		for _, e := range batch {
			slots := w.pending[e.nonce]
			if cur, ok := slots[e.slot]; ok && bytes.Equal(cur, e.sig) {
				delete(slots, e.slot)
				if len(slots) == 0 {
					delete(w.pending, e.nonce)
				}
			}
		}
	}
	w.running = false
	w.idle.Broadcast()
	w.mu.Unlock()
}

// pendingAt returns a copy of the not-yet-written signatures for nonce.
func (w *sigWriter) pendingAt(nonce uint64) map[uint32][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending[nonce]) == 0 {
		return nil
	}
	return maps.Clone(w.pending[nonce])
}

// flush blocks until every queued signature has been written.
func (w *sigWriter) flush() {
	w.mu.Lock()
	for w.running {
		w.idle.Wait()
	}
	w.mu.Unlock()
}
