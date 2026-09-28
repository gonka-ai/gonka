package session

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
)

type inflightMetaStore struct {
	storage.Storage
	inFlight atomic.Int32
	max      atomic.Int32
	release  chan struct{}
}

func (s *inflightMetaStore) GetSessionMeta(escrowID string) (*storage.SessionMeta, error) {
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		old := s.max.Load()
		if n <= old || s.max.CompareAndSwap(old, n) {
			break
		}
	}
	<-s.release
	return s.Storage.GetSessionMeta(escrowID)
}

func TestRecoverSessions_UsesEightWorkers(t *testing.T) {
	const sessions = 16
	inner := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	for i := 1; i <= sessions; i++ {
		require.NoError(t, inner.CreateSession(storage.CreateSessionParams{
			EscrowID:       strconv.Itoa(i),
			EpochID:        7,
			Version:        testutil.RuntimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         defaultConfig(3),
			Group:          group,
			InitialBalance: 100000,
		}))
	}

	store := &inflightMetaStore{Storage: inner, release: make(chan struct{})}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{
		escrow: &bridge.EscrowInfo{EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses},
	}, nil, nil))

	done := make(chan error, 1)
	go func() { done <- mgr.RecoverSessions() }()

	deadline := time.After(3 * time.Second)
	for store.max.Load() < int32(recoverSessionsConcurrency) {
		select {
		case <-deadline:
			t.Fatalf("recovery never reached %d concurrent workers, max=%d", recoverSessionsConcurrency, store.max.Load())
		case err := <-done:
			t.Fatalf("RecoverSessions returned before workers were saturated: max=%d err=%v", store.max.Load(), err)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(store.release)
	require.NoError(t, <-done)
	require.Equal(t, int32(recoverSessionsConcurrency), store.max.Load())

	mgr.sessionsMutex.RLock()
	defer mgr.sessionsMutex.RUnlock()
	require.Len(t, mgr.sessions, sessions)
}

func TestRecoverSessions_CapsWorkersToSessionCount(t *testing.T) {
	inner := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	require.NoError(t, inner.CreateSession(storage.CreateSessionParams{
		EscrowID:       "1",
		EpochID:        7,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         defaultConfig(3),
		Group:          group,
		InitialBalance: 100000,
	}))
	require.NoError(t, inner.CreateSession(storage.CreateSessionParams{
		EscrowID:       "2",
		EpochID:        7,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         defaultConfig(3),
		Group:          group,
		InitialBalance: 100000,
	}))

	store := &inflightMetaStore{Storage: inner, release: make(chan struct{})}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{
		escrow: &bridge.EscrowInfo{EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses},
	}, nil, nil))

	done := make(chan error, 1)
	go func() { done <- mgr.RecoverSessions() }()

	deadline := time.After(3 * time.Second)
	for store.max.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("recovery never reached 2 concurrent workers, max=%d", store.max.Load())
		case err := <-done:
			t.Fatalf("RecoverSessions returned before both sessions overlapped: max=%d err=%v", store.max.Load(), err)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(store.release)
	require.NoError(t, <-done)
	require.Equal(t, int32(2), store.max.Load(), "must not spawn more workers than sessions")
}
