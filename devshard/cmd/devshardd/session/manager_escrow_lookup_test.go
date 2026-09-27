package session

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport/rpcpb"
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
	_, err = store.GetSessionMeta("9987")
	require.ErrorIs(t, err, storage.ErrSessionNotFound, "Attach door must not CreateSession")
}

// epochClockStore is the retention clock NewHostManager's repair gate forwards.
type epochClockStore struct {
	storage.Storage
	epoch uint64
}

func (s epochClockStore) CurrentEpochID() uint64 { return s.epoch }

func (s epochClockStore) PruneCutoff() uint64 {
	return storage.RetentionCutoff(s.epoch, storage.DefaultEpochRetain)
}

func newRosterManager(t *testing.T, epoch uint64, br bridge.MainnetBridge) *HostManager {
	t.Helper()
	store := epochClockStore{Storage: storage.NewMemory(), epoch: epoch}
	return waitRecoveryRepairsOnCleanup(t, NewHostManager(store, mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
}

func overrideLookupTTL(t *testing.T, ttl time.Duration) {
	t.Helper()
	prev := unknownEscrowLookupTTL
	unknownEscrowLookupTTL = ttl
	t.Cleanup(func() { unknownEscrowLookupTTL = prev })
}

func rosterInfo(id string, epoch uint64, owner, slot string) *bridge.EscrowInfo {
	return &bridge.EscrowInfo{
		EscrowID:       id,
		CreatorAddress: owner,
		Slots:          []string{slot},
		EpochID:        epoch,
	}
}

func TestRoster_CurrentEpochOpensDoorWithoutReload(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const id = "9100"
	slot := "gonka1slot"
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
		id: rosterInfo(id, current, "gonka1owner", slot),
	}}
	mgr := newRosterManager(t, current, inner)
	ctx := rpcserver.WithEscrowID(context.Background(), id)

	ok, err := mgr.allowRPCPeer(ctx, slot)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = mgr.allowRPCPeer(ctx, slot)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int32(1), inner.calls.Load())
	_, err = mgr.store.GetSessionMeta(id)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestRoster_PreviousEpochOpensDoorAndServesPayload(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const epoch = current - 1
	const id = "9116"
	slot := "gonka1slot"
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
		id: rosterInfo(id, epoch, "gonka1owner", slot),
	}}
	mgr := newRosterManager(t, current, inner)
	ctx := rpcserver.WithEscrowID(context.Background(), id)

	ok, err := mgr.allowRPCPeer(ctx, slot)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = mgr.allowRPCPeer(ctx, slot)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int32(1), inner.calls.Load())

	got, err := mgr.payloadRoster(id, slot)
	require.NoError(t, err)
	require.Equal(t, epoch, got.EpochID)
	require.Equal(t, int32(1), inner.calls.Load())
	_, err = mgr.store.GetSessionMeta(id)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestRoster_TwoEpochsBackCachedButClosed(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const epoch = current - 2
	const id = "9115"
	slot := "gonka1slot"
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
		id: rosterInfo(id, epoch, "gonka1owner", slot),
	}}
	mgr := newRosterManager(t, current, inner)
	ctx := rpcserver.WithEscrowID(context.Background(), id)

	ok, err := mgr.allowRPCPeer(ctx, slot)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = mgr.allowRPCPeer(ctx, slot)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, int32(1), inner.calls.Load())

	_, err = mgr.payloadRoster(id, slot)
	require.ErrorIs(t, err, errPayloadEpochClosed)
	require.Equal(t, int32(1), inner.calls.Load())
	_, err = mgr.store.GetSessionMeta(id)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestRoster_PrunedEpochIsNotCached(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const id = "9120"
	slot := "gonka1slot"
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
		id: rosterInfo(id, current-3, "gonka1owner", slot),
	}}
	mgr := newRosterManager(t, current, inner)
	ctx := rpcserver.WithEscrowID(context.Background(), id)

	ok, err := mgr.allowRPCPeer(ctx, slot)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = mgr.allowRPCPeer(ctx, slot)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, int32(2), inner.calls.Load())

	_, err = mgr.payloadRoster(id, slot)
	require.ErrorIs(t, err, storage.ErrEpochPruned)
	_, err = mgr.store.GetSessionMeta(id)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestPayloadRoster_SharesAttachLookupBudget(t *testing.T) {
	overrideLookupLimits(t, 2, 100)
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{}}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(storage.NewMemory(), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))
	peer := "gonka1attacker"

	_, err := mgr.payloadRoster("9130", peer)
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = mgr.payloadRoster("9131", peer)
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = fetchBind(mgr, "9132", peer)
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited)
	require.Equal(t, int32(2), inner.calls.Load())
}

func TestPayloadRoster_EligibleLoadsDoNotConsumeBudget(t *testing.T) {
	overrideLookupLimits(t, 2, 100)
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	slot := "gonka1slot"
	found := map[string]*bridge.EscrowInfo{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("914%d", i)
		found[id] = rosterInfo(id, current, "gonka1owner", slot)
	}
	inner := &countingEscrowBridge{found: found}
	mgr := newRosterManager(t, current, inner)

	for i := 0; i < 5; i++ {
		got, err := mgr.payloadRoster(fmt.Sprintf("914%d", i), slot)
		require.NoError(t, err)
		require.Equal(t, slot, got.Slots[0])
	}
	_, err := mgr.payloadRoster("914x", slot)
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = mgr.payloadRoster("914y", slot)
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = fetchBind(mgr, "914z", slot)
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited)
	require.Equal(t, int32(7), inner.calls.Load())
}

type warmKeyBridge struct {
	countingEscrowBridge
	warm string
}

func (b *warmKeyBridge) VerifyWarmKey(addr, granter string) (bool, error) {
	return addr == b.warm && granter != "", nil
}

func TestPayloadRoster_WarmKeyRefundsAttachBudget(t *testing.T) {
	overrideLookupLimits(t, 2, 100)
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	warm := "gonka1warm"
	found := map[string]*bridge.EscrowInfo{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("915%d", i)
		found[id] = rosterInfo(id, current-1, "gonka1owner", "gonka1slot")
	}
	inner := &warmKeyBridge{countingEscrowBridge: countingEscrowBridge{found: found}, warm: warm}
	mgr := newRosterManager(t, current, inner)

	for i := 0; i < 5; i++ {
		_, err := mgr.payloadRoster(fmt.Sprintf("915%d", i), warm)
		require.NoError(t, err)
	}
	ok, err := mgr.allowRPCPeer(rpcserver.WithEscrowID(context.Background(), "9150"), "gonka1slot")
	require.NoError(t, err)
	require.True(t, ok, "current-1 must open the Attach door so GetPayload can run")
	_, err = mgr.payloadRoster("915x", warm)
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = mgr.payloadRoster("915y", warm)
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = fetchBind(mgr, "915z", warm)
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited)
	require.Equal(t, int32(7), inner.calls.Load())
}

type countingWarmBridge struct {
	countingEscrowBridge
	warmCalls atomic.Int32
	warmErr   error
	warm      string
	delay     time.Duration
}

func (b *countingWarmBridge) VerifyWarmKey(addr, granter string) (bool, error) {
	b.warmCalls.Add(1)
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	if b.warmErr != nil {
		return false, b.warmErr
	}
	return addr == b.warm && granter != "", nil
}

func TestPayloadPeer_WarmDecisionIsCached(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const id = "9160"
	warm := "gonka1warm"
	inner := &countingWarmBridge{
		countingEscrowBridge: countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
			id: {EscrowID: id, CreatorAddress: "gonka1owner", Slots: []string{"gonka1a", "gonka1b", "gonka1c"}, EpochID: current},
		}},
		warm: warm,
	}
	mgr := newRosterManager(t, current, inner)

	got, err := mgr.payloadRoster(id, warm)
	require.NoError(t, err)
	require.Equal(t, id, got.EscrowID)
	require.Equal(t, int32(1), inner.warmCalls.Load(), "scan stops at the first matching slot")
	_, err = mgr.payloadRoster(id, warm)
	require.NoError(t, err)
	require.Equal(t, int32(1), inner.warmCalls.Load())
}

func TestPayloadPeer_DenyIsCached(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const id = "9161"
	inner := &countingWarmBridge{
		countingEscrowBridge: countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
			id: {EscrowID: id, CreatorAddress: "gonka1owner", Slots: []string{"gonka1a", "gonka1b", "gonka1c"}, EpochID: current},
		}},
	}
	mgr := newRosterManager(t, current, inner)
	peer := "gonka1stranger"

	_, err := mgr.payloadRoster(id, peer)
	require.ErrorIs(t, err, errPayloadPeerDenied)
	_, err = mgr.payloadRoster(id, peer)
	require.ErrorIs(t, err, errPayloadPeerDenied)
	require.Equal(t, int32(3), inner.warmCalls.Load())
	require.Equal(t, int32(1), inner.calls.Load())
}

func TestPayloadPeer_QueryErrorIsNotCached(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const id = "9162"
	inner := &countingWarmBridge{
		countingEscrowBridge: countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
			id: {EscrowID: id, CreatorAddress: "gonka1owner", Slots: []string{"gonka1a", "gonka1b"}, EpochID: current},
		}},
		warmErr: fmt.Errorf("chain down"),
	}
	mgr := newRosterManager(t, current, inner)
	peer := "gonka1warm"

	_, err := mgr.payloadRoster(id, peer)
	require.ErrorIs(t, err, errPayloadPeerDenied)
	_, err = mgr.payloadRoster(id, peer)
	require.ErrorIs(t, err, errPayloadPeerDenied)
	require.Equal(t, int32(4), inner.warmCalls.Load())
}

func TestPayloadPeer_ConcurrentWarmScanIsShared(t *testing.T) {
	overrideLookupLimits(t, 100, 100)
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const id = "9163"
	warm := "gonka1warm"
	inner := &countingWarmBridge{
		countingEscrowBridge: countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
			id: {EscrowID: id, CreatorAddress: "gonka1owner", Slots: []string{"gonka1slot"}, EpochID: current},
		}},
		warm:  warm,
		delay: 20 * time.Millisecond,
	}
	mgr := newRosterManager(t, current, inner)

	const n = 8
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := mgr.payloadRoster(id, warm)
			errCh <- err
		}()
	}
	for i := 0; i < n; i++ {
		require.NoError(t, <-errCh)
	}
	require.Equal(t, int32(1), inner.warmCalls.Load())
}

func TestPayloadPeer_SlotDoesNotQueryWarmKeys(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const id = "9170"
	slot := "gonka1slot"
	inner := &countingWarmBridge{
		countingEscrowBridge: countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
			id: rosterInfo(id, current, "gonka1owner", slot),
		}},
	}
	mgr := newRosterManager(t, current, inner)

	got, err := mgr.payloadRoster(id, slot)
	require.NoError(t, err)
	require.Equal(t, slot, got.Slots[0])
	_, err = mgr.payloadRoster(id, slot)
	require.NoError(t, err)
	require.Equal(t, int32(0), inner.warmCalls.Load())
	require.Equal(t, int32(1), inner.calls.Load())
	_, err = mgr.store.GetSessionMeta(id)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestPayloadRoster_CreatorOpensDoorButDoesNotServePayload(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const id = "9171"
	owner := "gonka1owner"
	inner := &countingWarmBridge{
		countingEscrowBridge: countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
			id: rosterInfo(id, current, owner, "gonka1slot"),
		}},
	}
	mgr := newRosterManager(t, current, inner)
	ctx := rpcserver.WithEscrowID(context.Background(), id)

	ok, err := mgr.allowRPCPeer(ctx, owner)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = mgr.payloadRoster(id, owner)
	require.ErrorIs(t, err, errPayloadPeerDenied)
	_, err = mgr.payloadRoster(id, owner)
	require.ErrorIs(t, err, errPayloadPeerDenied)
	require.Equal(t, int32(1), inner.warmCalls.Load())
	require.Equal(t, int32(1), inner.calls.Load())
	_, err = mgr.store.GetSessionMeta(id)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestPayloadRoster_SettledIsRejected(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const id = "9172"
	slot := "gonka1slot"
	info := rosterInfo(id, current, "gonka1owner", slot)
	info.Settled = true
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{id: info}}
	mgr := newRosterManager(t, current, inner)

	_, err := mgr.payloadRoster(id, slot)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)
	_, err = mgr.payloadRoster(id, slot)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)
	require.Equal(t, int32(2), inner.calls.Load(), "a settled escrow is not kept in the roster")
	_, err = mgr.store.GetSessionMeta(id)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestPayloadRoster_WarmedEscrowSkipsChain(t *testing.T) {
	const current = uint64(7)
	const id = "9173"
	slot := "gonka1slot"
	mem := storage.NewMemory()
	require.NoError(t, mem.PutEscrowCache(storage.EscrowCacheInfo{
		EscrowID:       id,
		CreatorAddress: "gonka1owner",
		Slots:          []string{slot},
		EpochID:        current,
		CachedAt:       time.Now().Unix(),
	}))
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{}}
	store := epochClockStore{Storage: mem, epoch: current}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))

	got, err := mgr.payloadRoster(id, slot)
	require.NoError(t, err)
	require.Equal(t, current, got.EpochID)
	_, err = mgr.payloadRoster(id, slot)
	require.NoError(t, err)
	require.Equal(t, int32(0), inner.calls.Load())
	_, err = mgr.store.GetSessionMeta(id)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestRoster_FutureEpochCachedButClosed(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	const id = "9174"
	slot := "gonka1slot"
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
		id: rosterInfo(id, current+1, "gonka1owner", slot),
	}}
	mgr := newRosterManager(t, current, inner)
	ctx := rpcserver.WithEscrowID(context.Background(), id)

	ok, err := mgr.allowRPCPeer(ctx, slot)
	require.NoError(t, err)
	require.False(t, ok)
	_, err = mgr.payloadRoster(id, slot)
	require.ErrorIs(t, err, errPayloadEpochClosed)
	ok, err = mgr.allowRPCPeer(ctx, slot)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, int32(1), inner.calls.Load())
}

func TestServeUnboundGetPayload_GuardRejectsBeforeFetch(t *testing.T) {
	overrideLookupTTL(t, 0)
	const current = uint64(7)
	slot := "gonka1slot"
	closedID := "9175"
	openID := "9176"
	inner := &countingEscrowBridge{found: map[string]*bridge.EscrowInfo{
		closedID: rosterInfo(closedID, current-2, "gonka1owner", slot),
		openID:   rosterInfo(openID, current, "gonka1owner", slot),
	}}
	mgr := newRosterManager(t, current, inner)
	req := &rpcpb.GetPayloadRequest{InferenceId: "1"}

	_, err := mgr.ServeUnboundGetPayload(rpcserver.WithEscrowID(context.Background(), closedID), slot, req)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "escrow epoch is not open for payload")

	_, err = mgr.ServeUnboundGetPayload(rpcserver.WithEscrowID(context.Background(), openID), "gonka1stranger", req)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Contains(t, err.Error(), "peer is not a known participant")

	_, err = mgr.store.GetSessionMeta(closedID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
	_, err = mgr.store.GetSessionMeta(openID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}
