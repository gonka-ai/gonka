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

	// workerCtx is the parent context for in-flight DeleteInference calls. It
	// stays alive across normal shutdown so the queue actually drains;
	// shutdown cancels it only after the caller's outer context times out,
	// so a slow storage backend cannot outlive HostManager.Close.
	workerCtx    context.Context
	workerCancel context.CancelFunc

	// mu coordinates send-vs-close on queue. Senders take RLock and check
	// closed before sending; shutdown takes the write lock to flip closed
	// and close the channel atomically. RLock contention with the host is
	// limited to the (very short) shutdown window.
	mu     sync.RWMutex
	closed bool
	queue  chan pruneJob

	wg           sync.WaitGroup
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
		workerCtx:     ctx,
		workerCancel:  cancel,
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
// epoch PruneEpoch as backstop. Safe to call concurrently with shutdown:
// senders check the closed flag under RLock so send-on-closed-channel is not
// possible.
func (s *payloadPruneSink) OnInferencePrunable(event host.InferencePruneEvent) {
	if s == nil || s.store == nil {
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

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.queue <- job:
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
	ctx, cancel := context.WithTimeout(s.workerCtx, pruneDeleteTimeout)
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

// shutdown stops accepting new work and drains the queue. Workers complete
// their current and queued deletes using workerCtx (not cancelled here) so
// the drain is honored. If the caller's ctx fires before drain completes,
// workerCtx is cancelled to abort in-flight DeleteInference calls and the
// method returns ctx.Err(); a final wg.Wait keeps workers from outliving the
// payload store close that follows HostManager.Close. Idempotent.
func (s *payloadPruneSink) shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var waitErr error
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.queue)
		s.mu.Unlock()

		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			s.workerCancel()
			return
		case <-ctx.Done():
			waitErr = ctx.Err()
		}

		s.workerCancel()
		s.wg.Wait()
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
