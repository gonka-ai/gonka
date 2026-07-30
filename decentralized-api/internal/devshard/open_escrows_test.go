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

func TestHostManager_WarmEscrow_CachesWithoutSession(t *testing.T) {
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
	require.Equal(t, 0, mgr.OpenEscrowCount(), "pre-init must not open a session")
	require.Equal(t, 1, br.getEscrowCalls)

	cached, err := store.GetEscrowCache("escrow-warm")
	require.NoError(t, err)
	require.Equal(t, uint64(100000), cached.Amount)

	require.NoError(t, mgr.WarmEscrow("escrow-warm"))
	require.Equal(t, 0, mgr.OpenEscrowCount())
	require.Equal(t, 1, br.getEscrowCalls, "duplicate warm must reuse escrow cache")

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Empty(t, active)
}

func TestHostManager_WarmEscrow_ThenCreateUsesCache(t *testing.T) {
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

	require.NoError(t, mgr.WarmEscrow("escrow-race"))
	require.Equal(t, 1, br.getEscrowCalls)

	_, err := mgr.getOrCreate("escrow-race")
	require.NoError(t, err)
	require.Equal(t, 1, br.getEscrowCalls, "create must use escrow cache")
	require.Equal(t, 1, mgr.OpenEscrowCount())

	_, err = store.GetEscrowCache("escrow-race")
	require.ErrorIs(t, err, storage.ErrEscrowCacheNotFound, "cache consumed after CreateSession")
}

func TestHostManager_WarmEscrow_ConcurrentSingleflight(t *testing.T) {
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
			EscrowID:       "escrow-sf",
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
		require.NoError(t, mgr.WarmEscrow("escrow-sf"))
	}()
	go func() {
		defer wg.Done()
		require.NoError(t, mgr.WarmEscrow("escrow-sf"))
	}()
	wg.Wait()

	require.Equal(t, 1, br.getEscrowCalls)
	require.Equal(t, 0, mgr.OpenEscrowCount())
}

func TestHostManager_OnEscrowSettled_ClearsCache(t *testing.T) {
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
	_, err := store.GetEscrowCache("escrow-settle")
	require.NoError(t, err)

	require.NoError(t, mgr.OnEscrowSettled("escrow-settle"))
	_, err = store.GetEscrowCache("escrow-settle")
	require.ErrorIs(t, err, storage.ErrEscrowCacheNotFound)
	require.Equal(t, 0, mgr.OpenEscrowCount())

	require.NoError(t, mgr.OnEscrowSettled("escrow-settle"))
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
	_, err := store.GetEscrowCache("escrow-out")
	require.ErrorIs(t, err, storage.ErrEscrowCacheNotFound)
}

func TestHostManager_WarmEscrow_NoVersionClaimAcrossBoundVersions(t *testing.T) {
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
			EscrowID:       "escrow-multi",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
			EpochID:        7,
		},
	}
	mgrA := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), "v-a", br, nil, nil)
	mgrB := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), "v-b", br, nil, nil)
	mgrA.SetReady()
	mgrB.SetReady()
	t.Cleanup(func() {
		_ = mgrA.Close()
		_ = mgrB.Close()
	})

	require.NoError(t, mgrA.WarmEscrow("escrow-multi"))
	require.NoError(t, mgrB.WarmEscrow("escrow-multi"))

	_, err := mgrA.getOrCreate("escrow-multi")
	require.NoError(t, err)

	_, err = mgrB.getOrCreate("escrow-multi")
	require.ErrorIs(t, err, storage.ErrSessionVersionConflict,
		"only the first CreateSession claims runtime version")
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

func TestHostManager_EvictBefore_ScrubsOrphanOpenSet(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-live",
		EpochID:        7,
		Version:        runtimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000000,
	}))

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.RecoverSessions())
	t.Cleanup(func() { _ = mgr.Close() })
	require.Equal(t, 1, mgr.OpenEscrowCount())

	// Simulate a leak: openSet entry with no live session.
	mgr.openSet["ghost-escrow"] = struct{}{}
	require.Equal(t, 2, mgr.OpenEscrowCount())

	// cutoff that does not evict the live session still runs the orphan scrub.
	require.Equal(t, 0, mgr.EvictBefore(1))
	require.Equal(t, 1, mgr.OpenEscrowCount())
}
