package user

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
)

// gatedStore blocks AddSignature until gate is closed; afterGet runs after
// each GetSignatures.
type gatedStore struct {
	storage.Storage
	gate     chan struct{}
	calls    atomic.Int32
	afterGet func()
}

func (g *gatedStore) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	sigs, err := g.Storage.GetSignatures(escrowID, nonce)
	if g.afterGet != nil {
		g.afterGet()
	}
	return sigs, err
}

func (g *gatedStore) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	<-g.gate
	g.calls.Add(1)
	return g.Storage.AddSignature(escrowID, nonce, slotID, sig)
}

func setupGatedSession(t *testing.T, numHosts int) (*Session, *gatedStore) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	db := newTestStore(t)
	require.NoError(t, db.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, CreatorAddr: user.Address(),
		Config: config, Group: group, InitialBalance: 100000,
	}))
	store := &gatedStore{Storage: db, gate: make(chan struct{})}
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier, WithStorage(store))
	require.NoError(t, err)
	return session, store
}

// A slow signature write must not hold the escrow's requests (#1986).
func TestSession_SignatureWriteDoesNotBlockRequests(t *testing.T) {
	session, store := setupGatedSession(t, 3)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	done := make(chan error, 1)
	go func() {
		for i := 0; i < 4; i++ {
			if _, err := session.SendInference(context.Background(), params); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		close(store.gate)
		t.Fatal("requests waited for a blocked signature write")
	}
	require.Zero(t, store.calls.Load(), "no write can land while the gate is closed")

	// A nonce trimmed from memory still reads complete from the queue.
	session.mu.Lock()
	var nonce uint64
	for n, sigs := range session.signatures {
		if len(sigs) > 0 {
			nonce = n
			break
		}
	}
	require.NotZero(t, nonce)
	want := len(session.signatures[nonce])
	delete(session.signatures, nonce)
	session.sigsTrimmedThrough = max(session.sigsTrimmedThrough, nonce)
	require.Len(t, session.signaturesAtLocked(nonce), want)
	session.mu.Unlock()

	close(store.gate)
	require.NoError(t, session.Close())
	require.Positive(t, store.calls.Load())
	stored, err := store.Storage.GetSignatures("escrow-1", nonce)
	require.NoError(t, err)
	require.Len(t, stored, want, "Close flushes queued signatures")
}

func TestSigWriter_KeepsOrderAndNewestPending(t *testing.T) {
	_, store := setupGatedSession(t, 1)
	w := newSigWriter(store, "escrow-1")
	w.enqueue(1, 0, []byte{1})
	w.enqueue(1, 0, []byte{2})
	require.Equal(t, []byte{2}, w.pendingAt(1)[0])
	close(store.gate)
	w.flush()
	require.Nil(t, w.pendingAt(1))
	got, err := store.Storage.GetSignatures("escrow-1", 1)
	require.NoError(t, err)
	require.Equal(t, []byte{2}, got[0], "the later write wins")
}

// A write that lands between the store read and the queue read must not
// drop the signature of a trimmed nonce.
func TestSession_TrimmedReadSeesWriteLandingMidRead(t *testing.T) {
	session, store := setupGatedSession(t, 1)
	session.mu.Lock()
	session.persistSignatureLocked(7, 0, []byte{7})
	store.afterGet = func() {
		close(store.gate)
		session.sigWriter.flush()
	}
	session.sigsTrimmedThrough = 7
	got := session.signaturesAtLocked(7)
	session.mu.Unlock()
	require.Equal(t, []byte{7}, got[0])
}
