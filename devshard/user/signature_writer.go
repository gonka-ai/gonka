package user

import (
	"sync"
	"time"

	"devshard/logging"
	"devshard/storage"
)

const signatureQueueLimit = 4096

type signatureWrite struct {
	nonce     uint64
	slot      uint32
	signature []byte
}

type signatureWriter struct {
	store    storage.Storage
	escrowID string
	limit    int
	timeStep func(step string, started time.Time)

	mu      sync.Mutex
	idle    sync.Cond
	queue   []signatureWrite
	running bool
}

func newSignatureWriter(store storage.Storage, escrowID string, timeStep func(step string, started time.Time)) *signatureWriter {
	writer := &signatureWriter{store: store, escrowID: escrowID, limit: signatureQueueLimit, timeStep: timeStep}
	writer.idle.L = &writer.mu
	return writer
}

func (w *signatureWriter) add(nonce uint64, slot uint32, signature []byte) {
	write := signatureWrite{nonce: nonce, slot: slot, signature: signature}
	w.mu.Lock()
	if len(w.queue) >= w.limit {
		w.mu.Unlock()
		w.persist(write)
		return
	}
	w.queue = append(w.queue, write)
	if !w.running {
		w.running = true
		go w.drain()
	}
	w.mu.Unlock()
}

func (w *signatureWriter) drain() {
	for {
		w.mu.Lock()
		batch := w.queue
		w.queue = nil
		if len(batch) == 0 {
			w.running = false
			w.idle.Broadcast()
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
		for _, write := range batch {
			w.persist(write)
		}
	}
}

func (w *signatureWriter) persist(write signatureWrite) {
	defer w.timeStep(StepStoreAddSignature, time.Now())
	if err := w.store.AddSignature(w.escrowID, write.nonce, write.slot, write.signature); err != nil {
		logging.Warn("failed to persist signature",
			"escrow_id", w.escrowID, "nonce", write.nonce, "slot", write.slot, "error", err)
	}
}

func (w *signatureWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.running {
		w.idle.Wait()
	}
}
