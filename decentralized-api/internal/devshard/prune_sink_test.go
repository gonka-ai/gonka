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
	mu        sync.Mutex
	started   chan struct{}
	release   chan struct{}
	deleteCnt atomic.Int32
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
