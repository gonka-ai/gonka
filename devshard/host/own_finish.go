package host

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"devshard/logging"
	"devshard/storage"
	"devshard/types"
)

func (h *Host) ownFinishStore() (storage.OwnFinishStore, bool) {
	if h.store == nil {
		return nil, false
	}
	store, ok := storage.AsOwnFinishStore(h.store, h.escrowID)
	if !ok && h.ownFinishStoreMissing.CompareAndSwap(false, true) {
		logging.Warn("storage keeps no own finishes; a restart loses unsent finishes", "subsystem", "host", "escrow_id", h.escrowID, "store", fmt.Sprintf("%T", h.store))
	}
	return store, ok
}

func (h *Host) persistOwnFinishAsync(inferenceID uint64, tx *types.DevshardTx) {
	store, ok := h.ownFinishStore()
	if !ok {
		return
	}
	data, err := proto.Marshal(tx)
	if err != nil {
		logging.Warn("failed to marshal own finish", "subsystem", "host", "escrow_id", h.escrowID, "inference_id", inferenceID, "error", err)
		return
	}
	h.ownFinishWrites.Add(1)
	go func() {
		defer h.ownFinishWrites.Done()
		if err := store.SaveOwnFinish(h.escrowID, inferenceID, data); err != nil {
			logging.Warn("failed to persist own finish", "subsystem", "host", "escrow_id", h.escrowID, "inference_id", inferenceID, "error", err)
		}
	}()
}

func (h *Host) reloadOwnFinishesIfDue() {
	if h.ownFinishReloadDue.CompareAndSwap(true, false) {
		h.reloadOwnFinishes()
	}
}

func (h *Host) reloadOwnFinishes() {
	store, ok := h.ownFinishStore()
	if !ok {
		return
	}
	h.mu.Lock()
	ids := h.ownFinishCandidatesLocked()
	h.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	found, err := store.LoadOwnFinishes(h.escrowID, ids)
	if err != nil {
		logging.Warn("failed to load own finishes", "subsystem", "host", "escrow_id", h.escrowID, "count", len(ids), "error", err)
		return
	}
	if len(found) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	queued := queuedFinishIDs(h.mempool.Txs())
	added := 0
	for id, data := range found {
		if !h.ownFinishCandidateLocked(id, queued) {
			continue
		}
		tx := &types.DevshardTx{}
		if err := proto.Unmarshal(data, tx); err != nil {
			logging.Warn("failed to decode own finish", "subsystem", "host", "escrow_id", h.escrowID, "inference_id", id, "error", err)
			continue
		}
		finish := tx.GetFinishInference()
		if finish == nil || finish.InferenceId != id || !h.slotIDs[finish.ExecutorSlot] {
			continue
		}
		h.mempool.Add(MempoolEntry{Tx: tx, ProposedAt: h.sm.LatestNonce()})
		queued[id] = struct{}{}
		added++
	}
	if added > 0 {
		logging.Info("reloaded own finishes", "subsystem", "host", "escrow_id", h.escrowID, "count", added)
	}
}

func (h *Host) ownFinishCandidatesLocked() []uint64 {
	queued := queuedFinishIDs(h.mempool.Txs())
	var ids []uint64
	for id := range h.sm.LiveInferenceIDs() {
		if h.ownFinishCandidateLocked(id, queued) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *Host) ownFinishCandidateLocked(id uint64, queued map[uint64]struct{}) bool {
	if slot, ok := h.sm.InferenceExecutorSlot(id); !ok || !h.slotIDs[slot] {
		return false
	}
	if _, busy := h.executing[id]; busy {
		return false
	}
	if _, has := queued[id]; has {
		return false
	}
	rec, ok := h.sm.Inference(id)
	if !ok {
		return false
	}
	return rec.Status == types.StatusPending || rec.Status == types.StatusStarted
}

func queuedFinishIDs(txs []*types.DevshardTx) map[uint64]struct{} {
	out := make(map[uint64]struct{})
	for _, tx := range txs {
		if finish := tx.GetFinishInference(); finish != nil {
			out[finish.InferenceId] = struct{}{}
		}
	}
	return out
}
