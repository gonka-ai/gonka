package devshard

import (
	"context"
	"errors"
	"strconv"
	"time"

	"decentralized-api/logging"
	"decentralized-api/payloadstorage"

	inferenceTypes "github.com/productscience/inference/x/inference/types"

	"devshard/host"
	devshardserver "devshard/server"
	"devshard/storage"
)

// pruneDeleteTimeout caps the delete RPC so a slow backend cannot stall the
// prune queue. The actual delete is fire-and-forget; epoch sweep is a backstop.
const pruneDeleteTimeout = 5 * time.Second

// payloadPruneSink translates host.InferencePruneEvent into PayloadStorage
// DeleteInference calls. It is intentionally narrow: no caching, no retry
// queue, and no awareness of the host beyond the event itself. The
// ManagedStorage epoch sweep stays as the cleanup backstop.
type payloadPruneSink struct {
	store payloadstorage.PayloadStorage
	// fallbackEpoch is consulted when the host did not stamp an epoch on the
	// event. In practice this only happens when the host was constructed with
	// no WithEpochID (epochID == 0), so the supplier is best-effort.
	fallbackEpoch func() uint64
}

func newPayloadPruneSink(store payloadstorage.PayloadStorage, fallbackEpoch func() uint64) *payloadPruneSink {
	return &payloadPruneSink{store: store, fallbackEpoch: fallbackEpoch}
}

// OnInferencePrunable runs the delete asynchronously so the host mutex is
// never held across the storage I/O. ErrNotFound is treated as success
// because adjacent epochs and idempotent re-emission both make ErrNotFound a
// normal outcome.
func (s *payloadPruneSink) OnInferencePrunable(event host.InferencePruneEvent) {
	if s == nil || s.store == nil {
		return
	}
	primaryEpoch := uint64(0)
	if event.PayloadEpochKnown {
		primaryEpoch = event.PayloadEpoch
	} else if s.fallbackEpoch != nil {
		primaryEpoch = s.fallbackEpoch()
	}
	storageKey := devshardserver.PayloadKey(event.EscrowID, event.InferenceID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pruneDeleteTimeout)
		defer cancel()
		err := deletePayloadWithAdjacentEpochs(ctx, s.store, storageKey, primaryEpoch)
		if err == nil || errors.Is(err, payloadstorage.ErrNotFound) {
			return
		}
		logging.Warn("payload prune failed", inferenceTypes.PayloadStorage,
			"escrow_id", event.EscrowID,
			"inference_id", strconv.FormatUint(event.InferenceID, 10),
			"reason", event.Reason.String(),
			"epoch_id", primaryEpoch,
			"error", err,
		)
	}()
}

// deletePayloadWithAdjacentEpochs attempts the delete against primaryEpoch
// first and then probes primaryEpoch-1 / primaryEpoch+1. This mirrors the
// retrieval-side adjacent fallback so a payload that straddled an epoch
// boundary between Store and the prune event is still cleaned up. Returns
// nil if at least one epoch matched; ErrNotFound if every epoch reported
// missing; or the last non-ErrNotFound error otherwise.
func deletePayloadWithAdjacentEpochs(ctx context.Context, store payloadstorage.PayloadStorage, key string, primaryEpoch uint64) error {
	seen := make(map[uint64]struct{}, 3)
	candidates := make([]uint64, 0, 3)
	add := func(epoch uint64) {
		if _, ok := seen[epoch]; ok {
			return
		}
		seen[epoch] = struct{}{}
		candidates = append(candidates, epoch)
	}
	add(primaryEpoch)
	if primaryEpoch > 0 {
		add(primaryEpoch - 1)
	}
	add(primaryEpoch + 1)

	var lastErr error
	matched := false
	for _, epoch := range candidates {
		err := store.DeleteInference(ctx, key, epoch)
		switch {
		case err == nil:
			matched = true
		case errors.Is(err, payloadstorage.ErrNotFound):
			// expected: probe the remaining epochs
		default:
			lastErr = err
		}
	}
	if matched {
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return payloadstorage.ErrNotFound
}

// fallbackEpochFromStore exposes the same epoch resolver the retrieval path
// uses so the prune sink and the payload server agree on the meaning of
// "current epoch" when an event lacks PayloadEpoch.
func fallbackEpochFromStore(store storage.Storage) func() uint64 {
	return func() uint64 {
		return currentEpochIDFromStore(store)
	}
}
