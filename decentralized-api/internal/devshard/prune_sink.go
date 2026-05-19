package devshard

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"decentralized-api/logging"
	"decentralized-api/payloadstorage"

	inferenceTypes "github.com/productscience/inference/x/inference/types"

	"devshard/host"
	devshardserver "devshard/server"
	"devshard/storage"
)

const (
	// pruneDeleteTimeout caps each delete so a slow backend cannot stall a worker.
	pruneDeleteTimeout = 5 * time.Second
	// pruneWorkerCount bounds concurrent payload-storage deletes.
	pruneWorkerCount = 8
	// pruneQueueCapacity is the buffered enqueue depth before drops (epoch sweep backstop).
	pruneQueueCapacity = 256
	// pruneShutdownTimeout is how long HostManager.Close waits for in-flight deletes.
	pruneShutdownTimeout = 15 * time.Second
)

// payloadPruneSink translates host.InferencePruneEvent into PayloadStorage
// DeleteInference calls via a bounded worker pool. The host must not block
// on storage I/O; events are enqueued and workers run deletes asynchronously.
// ManagedStorage epoch sweep remains the cleanup backstop for dropped work.
type payloadPruneSink struct {
	store         payloadstorage.PayloadStorage
	fallbackEpoch func() uint64

	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	queue        chan pruneJob
	shutdownOnce sync.Once
}

type pruneJob struct {
	escrowID    string
	inferenceID uint64
	reason      host.PruneReason
	epochID     uint64
	storageKey  string
}

func newPayloadPruneSink(store payloadstorage.PayloadStorage, fallbackEpoch func() uint64) *payloadPruneSink {
	ctx, cancel := context.WithCancel(context.Background())
	s := &payloadPruneSink{
		store:         store,
		fallbackEpoch: fallbackEpoch,
		ctx:           ctx,
		cancel:        cancel,
		queue:         make(chan pruneJob, pruneQueueCapacity),
	}
	for i := 0; i < pruneWorkerCount; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	return s
}

// OnInferencePrunable enqueues a delete without blocking the host mutex.
// ErrNotFound on delete is success; a full queue drops the job and relies on
// epoch PruneEpoch as backstop.
func (s *payloadPruneSink) OnInferencePrunable(event host.InferencePruneEvent) {
	if s == nil || s.store == nil {
		return
	}
	if s.ctx.Err() != nil {
		return
	}

	epochID := uint64(0)
	if event.PayloadEpochKnown {
		epochID = event.PayloadEpoch
	} else if s.fallbackEpoch != nil {
		epochID = s.fallbackEpoch()
	}

	job := pruneJob{
		escrowID:    event.EscrowID,
		inferenceID: event.InferenceID,
		reason:      event.Reason,
		epochID:     epochID,
		storageKey:  devshardserver.PayloadKey(event.EscrowID, event.InferenceID),
	}

	select {
	case s.queue <- job:
	case <-s.ctx.Done():
	default:
		logging.Warn("payload prune queue full, dropping event", inferenceTypes.PayloadStorage,
			"escrow_id", event.EscrowID,
			"inference_id", strconv.FormatUint(event.InferenceID, 10),
			"reason", event.Reason.String(),
			"epoch_id", epochID,
			"queue_capacity", pruneQueueCapacity,
		)
	}
}

func (s *payloadPruneSink) worker() {
	defer s.wg.Done()
	for job := range s.queue {
		s.runDelete(job)
	}
}

func (s *payloadPruneSink) runDelete(job pruneJob) {
	ctx, cancel := context.WithTimeout(s.ctx, pruneDeleteTimeout)
	defer cancel()

	err := deletePayloadAtEpoch(ctx, s.store, job.storageKey, job.epochID)
	if err == nil || errors.Is(err, payloadstorage.ErrNotFound) {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	logging.Warn("payload prune failed", inferenceTypes.PayloadStorage,
		"escrow_id", job.escrowID,
		"inference_id", strconv.FormatUint(job.inferenceID, 10),
		"reason", job.reason.String(),
		"epoch_id", job.epochID,
		"error", err,
	)
}

// shutdown stops accepting work, drains the queue, and waits for workers.
// It is safe to call more than once.
func (s *payloadPruneSink) shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var waitErr error
	s.shutdownOnce.Do(func() {
		s.cancel()
		close(s.queue)
		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	})
	return waitErr
}

// deletePayloadAtEpoch deletes the payload row in the partition keyed by
// epochID. Store and prune both use the host's pinned escrow epoch when
// PayloadEpochKnown; a wrong or missing partition returns ErrNotFound and
// ManagedStorage.PruneEpoch drops the row when that epoch is swept.
func deletePayloadAtEpoch(ctx context.Context, store payloadstorage.PayloadStorage, key string, epochID uint64) error {
	return store.DeleteInference(ctx, key, epochID)
}

// fallbackEpochFromStore exposes the same epoch resolver the retrieval path
// uses so the prune sink and the payload server agree on the meaning of
// "current epoch" when an event lacks PayloadEpoch.
func fallbackEpochFromStore(store storage.Storage) func() uint64 {
	return func() uint64 {
		return currentEpochIDFromStore(store)
	}
}
