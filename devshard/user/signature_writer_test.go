package user

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/storage"
)

// stalledSignatureStore holds every AddSignature until released, as a slow disk under the store does.
type stalledSignatureStore struct {
	*storage.Memory
	release chan struct{}
	mu      sync.Mutex
	written int
}

func newStalledSignatureStore() *stalledSignatureStore {
	return &stalledSignatureStore{Memory: storage.NewMemory(), release: make(chan struct{})}
}

func (s *stalledSignatureStore) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	<-s.release
	s.mu.Lock()
	s.written++
	s.mu.Unlock()
	return s.Memory.AddSignature(escrowID, nonce, slotID, sig)
}

func (s *stalledSignatureStore) signaturesWritten() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written
}

func signatureTestParams() InferenceParams {
	return InferenceParams{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
}

// Test flow:
//  1. Open a session whose store holds every signature write.
//  2. Serve two inferences while the writes are held.
//  3. Assert both are served: persisting a host's signature is not on the session lock, so a slow disk stalls no request.
func TestSession_ASlowSignatureWriteStallsNoRequest(t *testing.T) {
	store := newStalledSignatureStore()
	session := setupStoredSession(t, store)
	t.Cleanup(func() {
		close(store.release)
		require.NoError(t, session.Close())
	})

	served := make(chan error, 1)
	go func() {
		for range 2 {
			if _, err := session.SendInference(context.Background(), signatureTestParams()); err != nil {
				served <- err
				return
			}
		}
		served <- nil
	}()

	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("inferences waited on a held signature write")
	}
	require.Zero(t, store.signaturesWritten(), "precondition: no signature reached the store yet")
}

// Test flow:
//  1. Open a session whose store holds every signature write, and serve one inference.
//  2. Release the store and close the session.
//  3. Assert the signatures the inference collected reached the store before Close returned.
func TestSession_CloseWritesEveryQueuedSignature(t *testing.T) {
	store := newStalledSignatureStore()
	session := setupStoredSession(t, store)
	resp, err := session.SendInference(context.Background(), signatureTestParams())
	require.NoError(t, err)

	close(store.release)
	require.NoError(t, session.Close())

	require.Positive(t, store.signaturesWritten())
	stored, err := store.GetSignatures("escrow-1", resp.Nonce)
	require.NoError(t, err)
	require.NotEmpty(t, stored, "the host's signature at the served nonce is in the store")
}

// Test flow:
//  1. Open a session whose store holds every signature write, and queue every host's signature at nonce 2.
//  2. Move every cursor past nonce 2, so its signatures are read back from the store.
//  3. Check the quorum at nonce 2, releasing the store only once the check is waiting on it.
//  4. Assert the check saw the quorum: reading a trimmed nonce waits for the signatures still queued for the store.
func TestSession_ATrimmedNonceIsReadAfterItsQueuedSignaturesLand(t *testing.T) {
	store := newStalledSignatureStore()
	session := setupStoredSession(t, store)
	release := sync.OnceFunc(func() { close(store.release) })
	t.Cleanup(func() {
		release()
		require.NoError(t, session.Close())
	})
	roots := composeEmptyDiffs(t, session, 6)
	session.mu.Lock()
	for index := range session.group {
		for _, slot := range session.addrToSlots[session.group[index].ValidatorAddress] {
			session.persistSignatureLocked(2, slot, signSessionHostRootForTest(t, session, index, 2, roots[2]))
		}
		session.hostSyncNonce[index] = 6
	}
	session.dropDiffPrefixLocked()
	session.mu.Unlock()

	quorum := make(chan bool, 1)
	go func() { quorum <- session.hasQuorum(2, session.sm.QuorumThreshold()) }()
	select {
	case seen := <-quorum:
		require.True(t, seen, "the check read the store before the queued signatures reached it")
	case <-time.After(100 * time.Millisecond):
		release()
		require.True(t, <-quorum, "the check sees the signatures once they reach the store")
	}
}

type gatedSignatureStore struct {
	*storage.Memory
	entered chan uint64
	release chan struct{}
}

func (s *gatedSignatureStore) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	s.entered <- nonce
	<-s.release
	return s.Memory.AddSignature(escrowID, nonce, slotID, sig)
}

// Test flow:
//  1. Build a writer that queues at most two writes, over a store that holds every write.
//  2. Add one write and wait for it to reach the store, then queue two more behind it.
//  3. Add a fourth.
//  4. Assert the fourth reaches the store before add returns: a full queue pushes back on its caller instead of growing while the disk stalls.
func TestSignatureWriter_AFullQueueWritesBeforeAddReturns(t *testing.T) {
	store := &gatedSignatureStore{Memory: storage.NewMemory(), entered: make(chan uint64, 4), release: make(chan struct{})}
	t.Cleanup(sync.OnceFunc(func() { close(store.release) }))
	writer := newSignatureWriter(store, "escrow-1", func(string, time.Time) {})
	writer.limit = 2
	writer.add(1, 0, []byte("first"))
	require.Equal(t, uint64(1), <-store.entered, "precondition: the first write holds the drain")
	writer.add(2, 0, []byte("second"))
	writer.add(3, 0, []byte("third"))

	added := make(chan struct{})
	go func() {
		writer.add(4, 0, []byte("fourth"))
		close(added)
	}()

	select {
	case nonce := <-store.entered:
		require.Equal(t, uint64(4), nonce, "the write past the limit is the caller's own")
	case <-added:
		t.Fatal("add returned with the queue full before its write reached the store")
	}
}
