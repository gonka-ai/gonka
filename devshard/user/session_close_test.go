package user

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/storage"
)

// closeCountingStore is a storage.Storage that records how many times Close is
// called. The embedded storage.Storage is intentionally nil: this fake exists
// only to prove that Session.Close releases the underlying store, which is the
// resource the per-runtime memory leak was failing to free. Embedding the
// interface keeps the fake conformant as storage.Storage grows, so the only
// method with real behavior is Close; any other call would nil-panic, which is
// the correct signal for an unexpected use of this inert fake.
type closeCountingStore struct {
	storage.Storage
	closeCalls int
}

func (s *closeCountingStore) Close() error {
	s.closeCalls++
	return nil
}

// TestSession_Close_ClosesUnderlyingStore proves the resource-release leg of the
// leak fix: closing a Session must close the storage it owns. The gateway-side
// tests prove rt.close() is now invoked on every automatic deactivation path;
// this proves that invocation actually frees the SQLite store the session holds.
func TestSession_Close_ClosesUnderlyingStore(t *testing.T) {
	store := &closeCountingStore{}
	session, _, _ := setupSessionWithOptions(t, 1, 1_000_000, 0, WithStorage(store))

	require.NoError(t, session.Close())
	require.Equal(t, 1, store.closeCalls, "Session.Close must close the injected storage exactly once")
}
