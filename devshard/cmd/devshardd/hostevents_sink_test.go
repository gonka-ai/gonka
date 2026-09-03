package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	devshardstorage "devshard/storage"
	"devshard/types"
)

type sinkFakeBridge struct {
	bridge.MainnetBridge
	escrow *bridge.EscrowInfo
	err    error
}

func (f *sinkFakeBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.escrow, nil
}

func TestEscrowWarmSink_WarmEscrowPopulatesCache(t *testing.T) {
	store := devshardstorage.NewMemory()
	br := &sinkFakeBridge{escrow: &bridge.EscrowInfo{
		EscrowID: "1", CreatorAddress: "gonka1owner", EpochID: 3, Amount: 100, VoteThresholdFactor: 2,
	}}
	sink := newEscrowWarmSink(br, store, nil, nil)

	require.NoError(t, sink.WarmEscrow("1"))

	got, err := store.GetEscrowCache("1")
	require.NoError(t, err)
	require.Equal(t, "gonka1owner", got.CreatorAddress)
	require.Equal(t, uint64(3), got.EpochID)
	require.Equal(t, uint32(2), got.VoteThresholdFactor)
}

func TestEscrowWarmSink_WarmEscrowChainErrorDoesNotCache(t *testing.T) {
	store := devshardstorage.NewMemory()
	br := &sinkFakeBridge{err: errors.New("chain down")}
	sink := newEscrowWarmSink(br, store, nil, nil)

	require.Error(t, sink.WarmEscrow("1"))

	_, err := store.GetEscrowCache("1")
	require.ErrorIs(t, err, devshardstorage.ErrEscrowCacheNotFound)
}

func TestEscrowWarmSink_WarmSettledEscrowDoesNotCache(t *testing.T) {
	store := devshardstorage.NewMemory()
	require.NoError(t, store.PutEscrowCache(devshardstorage.EscrowCacheInfo{EscrowID: "1", EpochID: 3}))
	br := &sinkFakeBridge{escrow: &bridge.EscrowInfo{
		EscrowID: "1", CreatorAddress: "gonka1owner", EpochID: 3, Settled: true,
	}}
	var finalized []string
	sink := newEscrowWarmSink(br, store, nil, func(id string) error {
		finalized = append(finalized, id)
		return nil
	})

	require.NoError(t, sink.WarmEscrow("1"))

	_, err := store.GetEscrowCache("1")
	require.ErrorIs(t, err, devshardstorage.ErrEscrowCacheNotFound)
	require.Equal(t, []string{"1"}, finalized, "warming a settled escrow must finalize the session")
}

func TestEscrowWarmSink_OnEscrowSettledFinalizesSession(t *testing.T) {
	store := devshardstorage.NewMemory()
	var finalized []string
	sink := newEscrowWarmSink(&sinkFakeBridge{}, store, nil, func(id string) error {
		finalized = append(finalized, id)
		return nil
	})

	require.NoError(t, sink.OnEscrowSettled("7"))
	require.Equal(t, []string{"7"}, finalized)
}

func TestEscrowWarmSink_OnEscrowSettledPropagatesFinalizeError(t *testing.T) {
	store := devshardstorage.NewMemory()
	sink := newEscrowWarmSink(&sinkFakeBridge{}, store, nil, func(string) error {
		return errors.New("mark settled failed")
	})

	require.Error(t, sink.OnEscrowSettled("7"))
}

func TestEscrowWarmSink_OnEscrowSettledDropsCache(t *testing.T) {
	store := devshardstorage.NewMemory()
	require.NoError(t, store.PutEscrowCache(devshardstorage.EscrowCacheInfo{EscrowID: "1", EpochID: 1}))
	sink := newEscrowWarmSink(&sinkFakeBridge{}, store, nil, nil)

	require.NoError(t, sink.OnEscrowSettled("1"))

	_, err := store.GetEscrowCache("1")
	require.ErrorIs(t, err, devshardstorage.ErrEscrowCacheNotFound)
}

// perEscrowBridge answers GetEscrow from a fixed table so a rehydrate sweep can
// mix settled, open, and unreachable escrows in one pass.
type perEscrowBridge struct {
	bridge.MainnetBridge
	mu       sync.Mutex
	settled  map[string]bool
	errs     map[string]error
	calls    []string
	delay    time.Duration
	delayFor map[string]bool
}

func (b *perEscrowBridge) GetEscrow(id string) (*bridge.EscrowInfo, error) {
	b.mu.Lock()
	b.calls = append(b.calls, id)
	delay, wantErr := b.delay, b.errs[id]
	shouldDelay := delay > 0 && (b.delayFor == nil || b.delayFor[id])
	settled := b.settled[id]
	b.mu.Unlock()

	if shouldDelay {
		time.Sleep(delay)
	}
	if wantErr != nil {
		return nil, wantErr
	}
	return &bridge.EscrowInfo{EscrowID: id, Settled: settled}, nil
}

func (b *perEscrowBridge) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func newActiveSessionStore(t *testing.T, escrowIDs ...string) *devshardstorage.Memory {
	t.Helper()
	store := devshardstorage.NewMemory()
	for _, id := range escrowIDs {
		require.NoError(t, store.CreateSession(devshardstorage.CreateSessionParams{
			EscrowID:       id,
			EpochID:        1,
			CreatorAddr:    "gonka1owner",
			Group:          []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1host"}},
			InitialBalance: 1000,
			Version:        "test",
		}))
	}
	return store
}

// A settlement that landed while the host's cursor was unservable is gone from
// the dapi ring forever. The reset sweep is the only thing that notices.
func TestEscrowWarmSink_RehydrateFinalizesChainSettledSessions(t *testing.T) {
	store := newActiveSessionStore(t, "1", "2")
	br := &perEscrowBridge{settled: map[string]bool{"1": true}}
	var finalized []string
	sink := newEscrowWarmSink(br, store, nil, func(id string) error {
		finalized = append(finalized, id)
		return store.MarkSettled(id)
	})

	sink.RehydrateOpenEscrows()

	require.Equal(t, []string{"1"}, finalized)
	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "2", active[0].EscrowID)
}

func TestEscrowWarmSink_RehydrateKeepsSessionsWhenChainUnreachable(t *testing.T) {
	store := newActiveSessionStore(t, "1")
	br := &perEscrowBridge{errs: map[string]error{"1": errors.New("chain down")}}
	var finalized []string
	sink := newEscrowWarmSink(br, store, nil, func(id string) error {
		finalized = append(finalized, id)
		return nil
	})

	sink.RehydrateOpenEscrows()

	require.Empty(t, finalized, "a chain blip must not retire work the host bound")
	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 1)
}

// A dapi stuck in a reset loop would otherwise re-query every open escrow on
// every poll.
func TestEscrowWarmSink_RehydrateThrottlesRepeatSweeps(t *testing.T) {
	store := newActiveSessionStore(t, "1")
	br := &perEscrowBridge{}
	sink := newEscrowWarmSink(br, store, nil, nil)

	sink.RehydrateOpenEscrows()
	require.Equal(t, 1, br.callCount())

	sink.RehydrateOpenEscrows()
	require.Equal(t, 1, br.callCount(), "second sweep inside the throttle window must not re-query")
}

// An incomplete sweep leaves escrows unresolved, so the throttle must not defer
// the retry by a full interval.
func TestEscrowWarmSink_RehydrateRetriesAfterIncompleteSweep(t *testing.T) {
	store := newActiveSessionStore(t, "1")
	br := &perEscrowBridge{errs: map[string]error{"1": errors.New("chain down")}}
	sink := newEscrowWarmSink(br, store, nil, store.MarkSettled)

	sink.RehydrateOpenEscrows()
	require.Equal(t, 1, br.callCount())

	br.mu.Lock()
	br.errs = nil
	br.settled = map[string]bool{"1": true}
	br.mu.Unlock()

	sink.RehydrateOpenEscrows()
	require.Equal(t, 2, br.callCount(), "sweep that left escrows unresolved must be retryable at once")

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Empty(t, active)
}

// RehydrateOpenEscrows runs inline on the host-events loop, so a chain that
// accepts connections but never answers must not stall event delivery.
func TestEscrowWarmSink_RehydrateHungChainDoesNotStallEventLoop(t *testing.T) {
	store := newActiveSessionStore(t, "1")
	br := &perEscrowBridge{settled: map[string]bool{"1": true}, delay: time.Minute}
	var finalized []string
	sink := newEscrowWarmSink(br, store, nil, func(id string) error {
		finalized = append(finalized, id)
		return nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		sink.RehydrateOpenEscrows()
	}()

	select {
	case <-done:
	case <-time.After(rehydrateEscrowCheckTimeout + 10*time.Second):
		t.Fatal("rehydrate blocked on an unresponsive chain")
	}
	require.Empty(t, finalized, "a timed-out check must fail open")
}
