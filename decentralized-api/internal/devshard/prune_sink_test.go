package devshard

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"decentralized-api/payloadstorage"

	"devshard/host"

	"github.com/stretchr/testify/require"
)

type blockingPayloadStore struct {
	mu          sync.Mutex
	started     chan struct{}
	release     chan struct{}
	deleteCnt   atomic.Int32
	completeCnt atomic.Int32
}

func newBlockingPayloadStore() *blockingPayloadStore {
	return &blockingPayloadStore{
		started: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
}

func (s *blockingPayloadStore) Store(context.Context, string, uint64, []byte, []byte) error {
	return nil
}

func (s *blockingPayloadStore) Retrieve(context.Context, string, uint64) ([]byte, []byte, error) {
	return nil, nil, payloadstorage.ErrNotFound
}

func (s *blockingPayloadStore) PruneEpoch(context.Context, uint64) error {
	return nil
}

func (s *blockingPayloadStore) DeleteInference(ctx context.Context, inferenceID string, epochID uint64) error {
	s.deleteCnt.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		s.completeCnt.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestPayloadPruneSink_WorkersProcessEvents(t *testing.T) {
	store := newBlockingPayloadStore()
	sink := newPayloadPruneSink(store, nil)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), pruneShutdownTimeout)
		defer cancel()
		require.NoError(t, sink.shutdown(shutdownCtx))
	})

	sink.OnInferencePrunable(host.InferencePruneEvent{
		EscrowID:          "escrow-1",
		InferenceID:       7,
		Reason:            host.PruneReasonTerminal,
		PayloadEpoch:      9,
		PayloadEpochKnown: true,
	})

	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delete worker did not start")
	}
	close(store.release)

	require.Equal(t, int32(1), store.deleteCnt.Load())
}

func TestPayloadPruneSink_ShutdownDrainsQueue(t *testing.T) {
	store := newBlockingPayloadStore()
	sink := newPayloadPruneSink(store, nil)

	sink.OnInferencePrunable(host.InferencePruneEvent{
		EscrowID:          "escrow-1",
		InferenceID:       1,
		PayloadEpoch:      3,
		PayloadEpochKnown: true,
	})

	<-store.started
	close(store.release)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), pruneShutdownTimeout)
	defer cancel()
	require.NoError(t, sink.shutdown(shutdownCtx))
	require.Equal(t, int32(1), store.deleteCnt.Load())

	sink.OnInferencePrunable(host.InferencePruneEvent{
		EscrowID:          "escrow-1",
		InferenceID:       2,
		PayloadEpochKnown: true,
	})
	require.Equal(t, int32(1), store.deleteCnt.Load(), "no work after shutdown")
}

func TestPayloadPruneSink_ShutdownIdempotent(t *testing.T) {
	store := newBlockingPayloadStore()
	sink := newPayloadPruneSink(store, nil)
	close(store.release)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), pruneShutdownTimeout)
	defer cancel()
	require.NoError(t, sink.shutdown(shutdownCtx))
	require.NoError(t, sink.shutdown(shutdownCtx))
}

// TestPayloadPruneSink_ConcurrentSendDuringShutdown ensures concurrent
// OnInferencePrunable callers cannot panic by sending on a closed channel
// while shutdown is in progress. Without the RLock-protected `closed` flag
// the select-on-closed-chan send race produces a Go runtime panic.
func TestPayloadPruneSink_ConcurrentSendDuringShutdown(t *testing.T) {
	store := newBlockingPayloadStore()
	close(store.release)
	sink := newPayloadPruneSink(store, nil)

	const senders = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			<-start
			for j := 0; j < 64; j++ {
				sink.OnInferencePrunable(host.InferencePruneEvent{
					EscrowID:          "escrow-1",
					InferenceID:       id*1000 + uint64(j),
					PayloadEpoch:      5,
					PayloadEpochKnown: true,
				})
			}
		}(uint64(i))
	}

	close(start)
	time.Sleep(2 * time.Millisecond)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), pruneShutdownTimeout)
	defer cancel()
	require.NoError(t, sink.shutdown(shutdownCtx))
	wg.Wait()
}

// TestPayloadPruneSink_DrainsQueuedJobsUnderSlowBackend verifies that the
// queue actually drains after shutdown is called: jobs sitting in the buffer
// at shutdown-time must still hit DeleteInference. Pre-fix, workers used
// a context that shutdown cancelled before drain, so every queued job
// returned context.Canceled and was silently dropped.
func TestPayloadPruneSink_DrainsQueuedJobsUnderSlowBackend(t *testing.T) {
	store := newBlockingPayloadStore()
	sink := newPayloadPruneSink(store, nil)

	const total = 4
	for i := 0; i < total; i++ {
		sink.OnInferencePrunable(host.InferencePruneEvent{
			EscrowID:          "escrow-1",
			InferenceID:       uint64(i),
			PayloadEpoch:      5,
			PayloadEpochKnown: true,
		})
	}

	// Wait until at least one worker has started before signalling shutdown,
	// so shutdown observes the queue in a mid-drain state.
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("no delete started before shutdown")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), pruneShutdownTimeout)
		defer cancel()
		shutdownDone <- sink.shutdown(shutdownCtx)
	}()

	// Release deletes one-by-one so we can observe drain progress.
	for i := 0; i < total; i++ {
		store.release <- struct{}{}
	}

	select {
	case err := <-shutdownDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not complete after drain")
	}
	require.Equal(t, int32(total), store.completeCnt.Load(),
		"every queued job must complete during drain, not be cancelled")
}
