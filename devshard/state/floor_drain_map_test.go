package state

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/types"
)

// waitFloorSpy tracks EnsureHeights / HeaderTimeMs / Await while StoredAt
// stays empty until release — settlement wait must not fetch via HeaderTimeMs.
type waitFloorSpy struct {
	mu          sync.Mutex
	ms          int64
	ensureCalls [][]uint64
	headerCalls atomic.Int32
	awaitCalls  atomic.Int32
}

func (s *waitFloorSpy) StoredAt(uint64) (headertime.Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ms <= 0 {
		return headertime.Record{}, false
	}
	return headertime.Record{TimeMs: s.ms, Hash: []byte{0xa6}}, true
}

func (s *waitFloorSpy) HeaderTimeMs(uint64, []byte) (int64, error) {
	s.headerCalls.Add(1)
	return 0, types.ErrHeaderPending
}

func (s *waitFloorSpy) AwaitHeaderTimeMs(context.Context, uint64, []byte) (int64, error) {
	s.awaitCalls.Add(1)
	return 0, types.ErrHeaderPending
}

func (s *waitFloorSpy) EnsureHeights(heights []uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureCalls = append(s.ensureCalls, append([]uint64(nil), heights...))
}

func (s *waitFloorSpy) set(ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ms = ms
}

func (s *waitFloorSpy) takeEnsured() [][]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.ensureCalls
	s.ensureCalls = nil
	return out
}

func TestFloorTimeMs_MapPreferOverCarried(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0xa1}
	carried := time.Hour.Milliseconds()
	mapMs := (2 * time.Hour).Milliseconds()
	d.raiseWithTime(100, hash, carried)
	src.set(100, 2*time.Hour)

	var warns atomic.Int32
	d.sm.SetStampTimeMismatchHook(func(leg string, height uint64, _ []byte, stampMs, gotMap int64) {
		require.Equal(t, "floor", leg)
		require.Equal(t, uint64(100), height)
		require.Equal(t, carried, stampMs)
		require.Equal(t, mapMs, gotMap)
		warns.Add(1)
	})

	asOf := d.sm.LatestNonce() + 1
	d.sm.mu.Lock()
	p, err := d.sm.floorPointLocked(asOf)
	require.NoError(t, err)
	require.Equal(t, carried, p.TimeMs)
	got, err := d.sm.floorTimeMsLocked(p)
	d.sm.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, carried, got, "timeouts keep the carried floor time")
	require.Equal(t, int32(1), warns.Load(), "carried≠map warns; a later dispute rolls the timeout back")
}

func TestFloorTimeMs_CarriedWhenMapMisses(t *testing.T) {
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(headertime.New())
	hash := []byte{0xa2}
	carried := time.Hour.Milliseconds()
	d.raiseWithTime(100, hash, carried)

	asOf := d.sm.LatestNonce() + 1
	d.sm.mu.Lock()
	p, err := d.sm.floorPointLocked(asOf)
	require.NoError(t, err)
	got, err := d.sm.floorTimeMsLocked(p)
	d.sm.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, carried, got)
}

func TestDrain_UsesMapWhenCarriedFloorTimeZero(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0xa3}
	anchor := time.Hour
	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
	d.raiseWithTime(200, []byte{0xa4}, 0)

	dueMs := (anchor + time.Duration(d.sm.Config().RefusalTimeout)*time.Second).Milliseconds()
	src.set(200, time.Duration(dueMs)*time.Millisecond)

	sealNonce := d.next()
	d.sm.mu.Lock()
	err := d.sm.drainLiveIntoSealedAccLocked(sealNonce)
	_, live := d.sm.state.Inferences[id]
	d.sm.mu.Unlock()
	require.NoError(t, err, "map time at F replaces zero carried TimeMs")
	require.False(t, live)
}

func TestWaitFloorTimeIfMissing_NoWaitUnlessSettlementDrain(t *testing.T) {
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(headertime.New())
	d.sm.headerWait = time.Hour
	hash := []byte{0xa5}
	d.raiseWithTime(100, hash, 0)

	started := time.Now()
	require.NoError(t, d.sm.waitFloorTimeIfMissing(d.sm.LatestNonce()+1),
		"Active phase must not wait on a zero floor TimeMs")
	require.Less(t, time.Since(started), 200*time.Millisecond)
}

func TestWaitFloorTimeIfMissing_TimesOutOnSettlementDrain(t *testing.T) {
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(headertime.New())
	d.sm.headerWait = 30 * time.Millisecond
	hash := []byte{0xa6}
	d.raiseWithTime(100, hash, 0)

	d.sm.mu.Lock()
	d.sm.state.Phase = types.PhaseFinalizing
	d.sm.state.FinalizeNonce = 1
	nonce := d.sm.state.FinalizeNonce + uint64(len(d.sm.state.Group))
	d.sm.mu.Unlock()

	started := time.Now()
	require.NoError(t, d.sm.waitFloorTimeIfMissing(nonce))
	require.Less(t, time.Since(started), 200*time.Millisecond, "settlement does not wait for the map")
}

func TestWaitFloorTimeIfMissing_NoWaitWhenCarriedSet(t *testing.T) {
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(headertime.New())
	d.sm.headerWait = time.Hour
	hash := []byte{0xa7}
	d.raiseWithTime(100, hash, time.Hour.Milliseconds())

	d.sm.mu.Lock()
	d.sm.state.Phase = types.PhaseFinalizing
	d.sm.state.FinalizeNonce = 1
	nonce := d.sm.state.FinalizeNonce + uint64(len(d.sm.state.Group))
	d.sm.mu.Unlock()

	started := time.Now()
	require.NoError(t, d.sm.waitFloorTimeIfMissing(nonce))
	require.Less(t, time.Since(started), 200*time.Millisecond)
}

func TestWaitFloorTimeIfMissing_EnsureHeightsNotHeaderTimeMs(t *testing.T) {
	spy := &waitFloorSpy{}
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(spy)
	d.sm.headerWait = time.Second
	hash := []byte{0xa6}
	d.raiseWithTime(100, hash, 0)

	d.sm.mu.Lock()
	d.sm.state.Phase = types.PhaseFinalizing
	d.sm.state.FinalizeNonce = 1
	nonce := d.sm.state.FinalizeNonce + uint64(len(d.sm.state.Group))
	d.sm.mu.Unlock()

	started := time.Now()
	require.NoError(t, d.sm.waitFloorTimeIfMissing(nonce))
	require.Less(t, time.Since(started), 200*time.Millisecond)
	require.Empty(t, spy.takeEnsured(), "settlement does not ask the map for time(F)")
	require.Zero(t, spy.headerCalls.Load(), "must not call HeaderTimeMs")
	require.Zero(t, spy.awaitCalls.Load(), "must not call AwaitHeaderTimeMs")
}
