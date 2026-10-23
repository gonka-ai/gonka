package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type stalledAccountingStore struct {
	PerfStore
	release chan struct{}
}

func (s *stalledAccountingStore) UpsertAccountingAttempt(ctx context.Context, attempt RequestAccountingAttempt) error {
	<-s.release
	return s.PerfStore.UpsertAccountingAttempt(ctx, attempt)
}

func (s *stalledAccountingStore) InsertSample(ctx context.Context, sample RequestSample) error {
	<-s.release
	return s.PerfStore.InsertSample(ctx, sample)
}

// Test flow:
// 1. Back the tracker with a store whose attempt and sample writes block until released.
// 2. Record a request start, an attempt and a host sample, as the request path does before it dispatches.
// 3. Require every call to return while the store is still blocked.
// 4. Release the store and require the accounting read to see the attempt written before it.
func TestPerfTrackerKeepsTheRequestPathOffTheStore(t *testing.T) {
	sqliteStore, err := newSQLitePerfStore(filepath.Join(t.TempDir(), "perf.db"))
	require.NoError(t, err)
	store := &stalledAccountingStore{PerfStore: sqliteStore, release: make(chan struct{})}
	perf := NewPerfTracker(store)
	t.Cleanup(func() {
		perf.Close()
		require.NoError(t, sqliteStore.Close())
	})

	recorded := make(chan struct{})
	go func() {
		defer close(recorded)
		perf.RecordAccountingRequestStart("req-1", "42", "test-model", time.Unix(100, 0))
		perf.RecordAccountingAttempt(RequestAccountingAttempt{RequestID: "req-1", EscrowID: "42", Nonce: 7, HostIdx: 1, ParticipantKey: "host-a", CreatedAt: time.Unix(101, 0)})
		perf.Record(RequestSample{ParticipantKey: "host-a", HostIdx: 1, SendTime: time.Unix(101, 0)})
	}()
	select {
	case <-recorded:
	case <-time.After(5 * time.Second):
		close(store.release)
		t.Fatal("recording waited for a stalled store")
	}

	close(store.release)
	record, found, err := perf.FindAccountingRequest(context.Background(), "req-1", "42")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, record.Attempts, 1)
	require.Equal(t, uint64(7), record.Attempts[0].Nonce)
}

// Test flow:
// 1. Record an attempt on a tracker over a real store.
// 2. Close the tracker and require the write to have reached the store.
// 3. Require a write after Close to be dropped without a panic.
func TestPerfTrackerCloseDrainsQueuedWrites(t *testing.T) {
	store, err := newSQLitePerfStore(filepath.Join(t.TempDir(), "perf.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	perf := NewPerfTracker(store)
	perf.RecordAccountingRequestStart("req-1", "42", "test-model", time.Unix(100, 0))
	perf.RecordAccountingAttempt(RequestAccountingAttempt{RequestID: "req-1", EscrowID: "42", Nonce: 7, HostIdx: 1, ParticipantKey: "host-a", CreatedAt: time.Unix(101, 0)})

	perf.Close()

	record, found, err := store.FindAccountingRequest(context.Background(), "req-1", "42")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, record.Attempts, 1)
	require.NotPanics(t, func() {
		perf.RecordAccountingAttempt(RequestAccountingAttempt{RequestID: "req-1", EscrowID: "42", Nonce: 8})
	})
}
