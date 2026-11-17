package user

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/storage"
	"devshard/types"
)

// blockingDiffStore holds GetDiffs until release is closed once armed.
type blockingDiffStore struct {
	storage.Storage
	armed   bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingDiffStore) GetDiffs(escrowID string, from, to uint64) ([]types.DiffRecord, error) {
	if s.armed {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	return s.Storage.GetDiffs(escrowID, from, to)
}

func TestSession_CatchUpStoreReadDoesNotHoldTheSessionLock(t *testing.T) {
	store := &blockingDiffStore{Storage: storage.NewMemory(), entered: make(chan struct{}), release: make(chan struct{})}
	session := setupStoredSession(t, store)
	const n = maxRetainedDiffs + 40
	composeEmptyDiffs(t, session, n)
	store.armed = true

	done := make(chan error, 1)
	go func() { done <- session.sendCatchUpWith(context.Background(), 0, session.clients[0]) }()
	<-store.entered

	locked := make(chan struct{})
	go func() {
		session.mu.Lock()
		session.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		close(store.release)
		t.Fatal("a catch-up store read held Session.mu")
	}
	close(store.release)
	require.NoError(t, <-done)

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Equal(t, uint64(n), session.hostSyncNonce[0])
}
