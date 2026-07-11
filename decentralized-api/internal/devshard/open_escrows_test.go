package devshard

import (
	"sync"
	"testing"

	"devshard/bridge"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"

	"github.com/stretchr/testify/require"
)

func TestHostManager_WarmEscrow_IdempotentOpenSet(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, s := range hosts {
		addresses[i] = s.Address()
	}
	br := &countingBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-warm",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
			EpochID:        7,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetReady()
	t.Cleanup(func() { _ = mgr.Close() })

	require.NoError(t, mgr.WarmEscrow("escrow-warm"))
	require.Equal(t, 1, mgr.OpenEscrowCount())
	require.Equal(t, 1, br.getEscrowCalls)

	require.NoError(t, mgr.WarmEscrow("escrow-warm"))
	require.Equal(t, 1, mgr.OpenEscrowCount())
	require.Equal(t, 1, br.getEscrowCalls, "duplicate warm must reuse singleflight/session")
}

func TestHostManager_WarmEscrow_SharesSingleflightWithGetOrCreate(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, s := range hosts {
		addresses[i] = s.Address()
	}
	br := &countingBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-race",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
			EpochID:        7,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetReady()
	t.Cleanup(func() { _ = mgr.Close() })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		require.NoError(t, mgr.WarmEscrow("escrow-race"))
	}()
	go func() {
		defer wg.Done()
		_, err := mgr.getOrCreate("escrow-race")
		require.NoError(t, err)
	}()
	wg.Wait()

	require.Equal(t, 1, br.getEscrowCalls)
	require.Equal(t, 1, mgr.OpenEscrowCount())
}

func TestHostManager_OnEscrowSettled_RemovesFromOpenSet(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, s := range hosts {
		addresses[i] = s.Address()
	}
	br := &countingBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-settle",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
			EpochID:        7,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetReady()
	t.Cleanup(func() { _ = mgr.Close() })

	require.NoError(t, mgr.WarmEscrow("escrow-settle"))
	require.Equal(t, 1, mgr.OpenEscrowCount())

	require.NoError(t, mgr.OnEscrowSettled("escrow-settle"))
	require.Equal(t, 0, mgr.OpenEscrowCount())
	mgr.mu.RLock()
	_, ok := mgr.sessions["escrow-settle"]
	mgr.mu.RUnlock()
	require.False(t, ok)

	require.NoError(t, mgr.OnEscrowSettled("escrow-settle"))
	require.Equal(t, 0, mgr.OpenEscrowCount())
}

func TestHostManager_WarmEscrow_SoftFailsWhenNotInGroup(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	outsider := mustGenerateKey(t)
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, s := range hosts {
		addresses[i] = s.Address()
	}
	br := &countingBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-out",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
			EpochID:        7,
		},
	}
	mgr := NewHostManager(store, outsider, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetReady()
	t.Cleanup(func() { _ = mgr.Close() })

	require.NoError(t, mgr.WarmEscrow("escrow-out"), "not-in-group is soft-fail")
	require.Equal(t, 0, mgr.OpenEscrowCount())
}

func TestHostManager_EvictBefore_DropsOpenSet(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	for _, sess := range []struct {
		escrowID string
		epochID  uint64
	}{
		{escrowID: "escrow-old", epochID: 5},
		{escrowID: "escrow-new", epochID: 7},
	} {
		require.NoError(t, store.CreateSession(storage.CreateSessionParams{
			EscrowID:       sess.escrowID,
			EpochID:        sess.epochID,
			Version:        runtimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         config,
			Group:          group,
			InitialBalance: 100000000,
		}))
	}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.RecoverSessions())
	t.Cleanup(func() { _ = mgr.Close() })
	require.Equal(t, 2, mgr.OpenEscrowCount())

	require.Equal(t, 1, mgr.EvictBefore(6))
	require.Equal(t, 1, mgr.OpenEscrowCount())
}
