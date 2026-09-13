package session

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport/rpcserver"
)

type countingEscrowBridge struct {
	mockBridge
	calls atomic.Int32
	found map[string]*bridge.EscrowInfo
}

func (b *countingEscrowBridge) GetEscrow(id string) (*bridge.EscrowInfo, error) {
	b.calls.Add(1)
	if b.found != nil {
		if info, ok := b.found[id]; ok {
			return info, nil
		}
		return nil, bridge.ErrEscrowNotFound
	}
	return b.mockBridge.GetEscrow(id)
}

func fetchBind(m *HostManager, id, peer string) (*bridge.EscrowInfo, error) {
	return m.fetchEscrowForBind(id, peer)
}

func overrideLookupLimits(t *testing.T, peer, floor int) {
	t.Helper()
	prevPeer := unknownEscrowPerPeerPerMin
	prevFloor := unknownEscrowFloorPerMin
	unknownEscrowPerPeerPerMin = peer
	unknownEscrowFloorPerMin = floor
	t.Cleanup(func() {
		unknownEscrowPerPeerPerMin = prevPeer
		unknownEscrowFloorPerMin = prevFloor
	})
}

func TestFetchEscrowForBind_CachesNotFound(t *testing.T) {
	const escrowID = "9901"
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{}}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(storage.NewMemory(), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))

	_, err := fetchBind(mgr, escrowID, "gonka1peer")
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = fetchBind(mgr, escrowID, "gonka1peer")
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	require.Equal(t, int32(1), inner.calls.Load())
}

func TestFetchEscrowForBind_CachesSuccess(t *testing.T) {
	const escrowID = "9902"
	info := &bridge.EscrowInfo{EscrowID: escrowID, CreatorAddress: "gonka1owner", Slots: []string{"a"}}
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{escrowID: info}}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(storage.NewMemory(), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))

	got, err := fetchBind(mgr, escrowID, "gonka1peer")
	require.NoError(t, err)
	require.Equal(t, "gonka1owner", got.CreatorAddress)
	got.CreatorAddress = "mutated"
	again, err := fetchBind(mgr, escrowID, "gonka1peer")
	require.NoError(t, err)
	require.Equal(t, "gonka1owner", again.CreatorAddress)
	require.Equal(t, int32(1), inner.calls.Load())
}

func TestFetchEscrowForBind_RateLimitsUnknownIDs(t *testing.T) {
	overrideLookupLimits(t, 2, 100)

	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{}}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(storage.NewMemory(), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))
	peer := "gonka1attacker"

	_, err := fetchBind(mgr, "9910", peer)
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = fetchBind(mgr, "9911", peer)
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = fetchBind(mgr, "9912", peer)
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited)
	require.Equal(t, int32(2), inner.calls.Load())
}

func TestAllowRPCPeer_UnknownEscrowDoesNotRepeatGetEscrow(t *testing.T) {
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{}}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(storage.NewMemory(), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))
	ctx := rpcserver.WithEscrowID(context.Background(), "9920")

	ok, err := mgr.allowRPCPeer(ctx, "gonka1peer")
	require.False(t, ok)
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	ok, err = mgr.allowRPCPeer(ctx, "gonka1peer")
	require.False(t, ok)
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	require.Equal(t, int32(1), inner.calls.Load())
}

func TestFetchEscrowForBind_DoesNotCacheChainUnavailable(t *testing.T) {
	inner := &countingEscrowBridge{mockBridge: mockBridge{getEscrowErr: bridge.ErrChainUnavailable}}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(storage.NewMemory(), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))

	_, err := fetchBind(mgr, "9930", "gonka1peer")
	require.ErrorIs(t, err, bridge.ErrChainUnavailable)
	_, err = fetchBind(mgr, "9930", "gonka1peer")
	require.ErrorIs(t, err, bridge.ErrChainUnavailable)
	require.Equal(t, int32(2), inner.calls.Load())
}

func TestFetchEscrowForBind_FloorLimitsDistinctPeers(t *testing.T) {
	overrideLookupLimits(t, 100, 2)

	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{}}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(storage.NewMemory(), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))

	_, err := fetchBind(mgr, "9940", "gonka1a")
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = fetchBind(mgr, "9941", "gonka1b")
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = fetchBind(mgr, "9942", "gonka1c")
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited)
	require.Equal(t, int32(2), inner.calls.Load())
}

func TestUnknownEscrowLookupDefaults(t *testing.T) {
	require.Equal(t, 2, defaultUnknownEscrowPerPeerPerMin)
	require.Equal(t, 300, defaultUnknownEscrowFloorPerMin)
}

func TestEscrowLookupEligible(t *testing.T) {
	owner := "gonka1owner"
	slot := "gonka1slot"
	info := &bridge.EscrowInfo{CreatorAddress: owner, Slots: []string{slot, "gonka1other"}}

	require.True(t, escrowLookupEligible(info, nil, owner))
	require.True(t, escrowLookupEligible(info, nil, slot))
	require.True(t, escrowLookupEligible(info, bridge.ErrEscrowSettled, owner))
	require.False(t, escrowLookupEligible(info, nil, "gonka1stranger"))
	require.False(t, escrowLookupEligible(info, bridge.ErrChainUnavailable, owner))
	require.False(t, escrowLookupEligible(nil, nil, owner))
	require.False(t, escrowLookupEligible(info, nil, ""))
}

func TestFetchEscrowForBind_RefundsEligibleFirstBind(t *testing.T) {
	overrideLookupLimits(t, 2, 100)

	owner := "gonka1owner"
	slot := "gonka1slot"
	newFound := func() map[string]*bridge.EscrowInfo {
		found := map[string]*bridge.EscrowInfo{}
		for i := 0; i < 5; i++ {
			id := fmt.Sprintf("996%d", i)
			found[id] = &bridge.EscrowInfo{EscrowID: id, CreatorAddress: owner, Slots: []string{slot}}
		}
		return found
	}

	for _, peer := range []string{owner, slot} {
		t.Run(peer, func(t *testing.T) {
			inner := &countingEscrowBridge{found: newFound()}
			mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(storage.NewMemory(), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))
			for i := 0; i < 5; i++ {
				id := fmt.Sprintf("996%d", i)
				got, err := mgr.fetchEscrowForBind(id, peer)
				require.NoError(t, err)
				require.Equal(t, owner, got.CreatorAddress)
			}
			_, err := mgr.fetchEscrowForBind("996x", peer)
			require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
			_, err = mgr.fetchEscrowForBind("996y", peer)
			require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
			_, err = mgr.fetchEscrowForBind("996z", peer)
			require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited)
			require.Equal(t, int32(7), inner.calls.Load())
		})
	}
}

func TestFetchEscrowForBind_IneligibleRealEscrowConsumesBudget(t *testing.T) {
	overrideLookupLimits(t, 2, 100)

	found := map[string]*bridge.EscrowInfo{}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("997%d", i)
		found[id] = &bridge.EscrowInfo{EscrowID: id, CreatorAddress: "gonka1owner", Slots: []string{"gonka1slot"}}
	}
	inner := &countingEscrowBridge{found: found}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(storage.NewMemory(), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))
	stranger := "gonka1stranger"

	_, err := fetchBind(mgr, "9970", stranger)
	require.NoError(t, err)
	_, err = fetchBind(mgr, "9971", stranger)
	require.NoError(t, err)
	_, err = fetchBind(mgr, "9972", stranger)
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited)
	require.Equal(t, int32(2), inner.calls.Load())
}

func TestAllowRPCPeer_WarmedEscrowSkipsGetEscrow(t *testing.T) {
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{}}
	store := storage.NewMemory()
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))
	member := "gonka1member"
	require.NoError(t, store.PutEscrowCache(storage.EscrowCacheInfo{
		EscrowID:            "9987",
		CreatorAddress:      "gonka1owner",
		Slots:               []string{member, mgr.hostRPCAddress()},
		EpochID:             1,
		Amount:              100000,
		TokenPrice:          1,
		VoteThresholdFactor: 2,
	}))

	ok, err := mgr.allowRPCPeer(rpcserver.WithEscrowID(context.Background(), "9987"), member)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int32(0), inner.calls.Load(), "a warmed escrow must not look like a cold GetEscrow miss")
	meta, err := store.GetSessionMeta("9987")
	require.NoError(t, err)
	require.Equal(t, "gonka1owner", meta.CreatorAddr)
}
