package engine

import "sync"

type finishWakes struct {
	mu    sync.Mutex
	next  uint64
	calls map[heldWinnerKey]map[uint64]func()
}

func newFinishWakes() *finishWakes {
	return &finishWakes{calls: map[heldWinnerKey]map[uint64]func(){}}
}

func (w *finishWakes) subscriber(escrowID string, nonces []uint64) func(func()) func() {
	if w == nil || len(nonces) == 0 {
		return nil
	}
	return func(call func()) func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.next++
		id := w.next
		for _, nonce := range nonces {
			key := heldWinnerKey{escrowID: escrowID, nonce: nonce}
			if w.calls[key] == nil {
				w.calls[key] = map[uint64]func(){}
			}
			w.calls[key][id] = call
		}
		return func() { w.drop(escrowID, nonces, id) }
	}
}

func (w *finishWakes) drop(escrowID string, nonces []uint64, id uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, nonce := range nonces {
		key := heldWinnerKey{escrowID: escrowID, nonce: nonce}
		delete(w.calls[key], id)
		if len(w.calls[key]) == 0 {
			delete(w.calls, key)
		}
	}
}

func (w *finishWakes) wake(escrowID string, nonce uint64) {
	if w == nil {
		return
	}
	w.mu.Lock()
	var calls []func()
	for _, call := range w.calls[heldWinnerKey{escrowID: escrowID, nonce: nonce}] {
		calls = append(calls, call)
	}
	w.mu.Unlock()
	for _, call := range calls {
		go call()
	}
}

func openVoteNonces(steps []TimeoutStep, poster TimeoutPoster) []uint64 {
	if poster == nil {
		return nil
	}
	var nonces []uint64
	for _, step := range steps {
		if step.Post && step.Kind == SettleByVote {
			nonces = append(nonces, step.Nonce)
		}
	}
	return nonces
}
