package user

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"
)

// chatGateTimes is the gateway map the chat gate reads via StoredAt
// (LookupHeaderTimeMs → StampHeaderTime). HeaderTimeMs / AwaitHeaderTimeMs /
// GetBlockHeader are present so a call from the gate shows up in the counters.
type chatGateTimes struct {
	mu            sync.Mutex
	open          atomic.Bool
	ms            int64
	byHeight      map[uint64]int64
	headerCalls   atomic.Int32
	awaitCalls    atomic.Int32
	headerMsCalls atomic.Int32
}

func (g *chatGateTimes) set(height uint64, ms int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byHeight == nil {
		g.byHeight = make(map[uint64]int64)
	}
	g.byHeight[height] = ms
}

func (g *chatGateTimes) StoredAt(height uint64) (headertime.Record, bool) {
	if !g.open.Load() {
		return headertime.Record{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var ms int64
	if len(g.byHeight) > 0 {
		var ok bool
		ms, ok = g.byHeight[height]
		if !ok || ms <= 0 {
			return headertime.Record{}, false
		}
	} else if g.ms > 0 {
		ms = g.ms
	} else {
		return headertime.Record{}, false
	}
	// sessionOracles stamp hash is {0xaa}; StampHeaderTime rejects empty row hashes.
	return headertime.Record{TimeMs: ms, Hash: []byte{0xaa}}, true
}

func (g *chatGateTimes) HeaderTimeMs(height uint64, _ []byte) (int64, error) {
	g.headerMsCalls.Add(1)
	if !g.open.Load() {
		return 0, types.ErrHeaderPending
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.byHeight) > 0 {
		ms, ok := g.byHeight[height]
		if !ok || ms <= 0 {
			return 0, types.ErrHeaderPending
		}
		return ms, nil
	}
	if g.ms <= 0 {
		return 0, types.ErrHeaderPending
	}
	return g.ms, nil
}

func (g *chatGateTimes) AwaitHeaderTimeMs(context.Context, uint64, []byte) (int64, error) {
	g.awaitCalls.Add(1)
	return 0, types.ErrHeaderPending
}

func (g *chatGateTimes) GetBlockHeader(context.Context, uint64) (*blocks.Header, error) {
	g.headerCalls.Add(1)
	return nil, types.ErrHeaderPending
}

func (g *chatGateTimes) gateCalls() (header, await int32) {
	return g.headerCalls.Load(), g.awaitCalls.Load()
}

func openChatSession(t *testing.T, oracles []blocks.BlockOracle, times *chatGateTimes, extra ...SessionOption) *Session {
	t.Helper()
	return openChatSessionVersion(t, types.DevshardStateRootAndProtocolVersion, oracles, times, extra...)
}

func openChatSessionVersion(t *testing.T, version string, oracles []blocks.BlockOracle, times *chatGateTimes, extra ...SessionOption) *Session {
	t.Helper()
	var height uint64 = 100
	opts := extra
	session := setupFloorlessHeartbeatSessionVersion(t, version, &height, oracles, times, opts...)
	session.MarkHeightSeedGate(time.Now().Add(heightSeedGate))
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func freshChatMs() int64 {
	return time.Now().Add(-time.Second).UnixMilli()
}

func blockAcks(acks []*types.MsgHeightAck) int {
	n := 0
	for _, ack := range acks {
		if ack.ObservedHeight > 0 && len(ack.ObservedBlockHash) > 0 {
			n++
		}
	}
	return n
}

func TestFloorChat_OneAckIsNotReady(t *testing.T) {
	oracles, followers := sessionOracles(3, 100)
	followers[1].blind.Store(true)
	followers[2].blind.Store(true)
	times := &chatGateTimes{ms: freshChatMs()}
	times.open.Store(true)
	session := openChatSession(t, oracles, times)

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.Equal(t, 1, blockAcks(heightAcksInDiffs(seenDiffs(session))))
	floor, hash, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.Equal(t, uint64(100), floor)
	require.Equal(t, []byte{0xaa}, hash)

	beforeH, beforeA := times.gateCalls()
	beforeMs := times.headerMsCalls.Load()
	st := session.HeightSeedStatus()
	require.Equal(t, HeightSeedStatePending, st.State)
	require.Equal(t, 1, st.Seeded)
	require.Equal(t, 3, st.Slots)
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err := session.WaitFloorChat(waitCtx)
	cancel()
	var se *HeightSeedError
	require.ErrorAs(t, err, &se)
	require.Equal(t, beforeH, times.headerCalls.Load())
	require.Equal(t, beforeA, times.awaitCalls.Load())
	require.Equal(t, beforeMs, times.headerMsCalls.Load(), "chat gate must not call HeaderTimeMs")
}

func TestFloorChat_SecondAckOpensAndStartCopiesFloor(t *testing.T) {
	oracles, followers := sessionOracles(3, 100)
	followers[1].blind.Store(true)
	followers[2].blind.Store(true)
	wantMs := freshChatMs()
	times := &chatGateTimes{ms: wantMs}
	times.open.Store(true)
	session := openChatSession(t, oracles, times)
	ctx := context.Background()

	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)

	followers[1].blind.Store(false)
	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.GreaterOrEqual(t, blockAcks(heightAcksInDiffs(seenDiffs(session))), 2)

	beforeH, beforeA := times.gateCalls()
	beforeMs := times.headerMsCalls.Load()
	st := session.HeightSeedStatus()
	require.Equal(t, HeightSeedStateOK, st.State)
	require.GreaterOrEqual(t, st.Seeded, 2)
	require.Equal(t, beforeH, times.headerCalls.Load())
	require.Equal(t, beforeA, times.awaitCalls.Load())
	require.Equal(t, beforeMs, times.headerMsCalls.Load(), "chat gate must not call HeaderTimeMs")

	prepared, err := session.PrepareInference(ctx, InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	var start *types.MsgStartInference
	for _, tx := range prepared.diff.Txs {
		if s := tx.GetStartInference(); s != nil {
			start = s
		}
	}
	require.NotNil(t, start)
	require.Equal(t, uint64(100), start.ObservedHeight)
	require.Equal(t, []byte{0xaa}, start.ObservedBlockHash)
	require.Equal(t, wantMs, start.ObservedTimeMs, "compose fills stamp time from StoredAt")
	rec, ok := session.StateMachine().GetInference(prepared.Nonce())
	require.True(t, ok)
	require.Equal(t, uint64(100), rec.StartedAtHeight)
	require.Equal(t, wantMs, rec.StartedAtTimeMs)
	require.Equal(t, beforeH, times.headerCalls.Load())
	require.Equal(t, beforeA, times.awaitCalls.Load())
	require.Equal(t, beforeMs, times.headerMsCalls.Load(), "start compose must not call HeaderTimeMs")
}

func TestFloorChat_UnavailableAcksLeaveFloorEmpty(t *testing.T) {
	oracles, followers := sessionOracles(3, 100)
	for _, o := range followers {
		o.blind.Store(true)
	}
	times := &chatGateTimes{ms: freshChatMs()}
	times.open.Store(true)
	session := openChatSession(t, oracles, times)

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	acks := heightAcksInDiffs(seenDiffs(session))
	unavailable := 0
	for _, ack := range acks {
		if ack.SyncState == types.SyncState_ORACLE_UNAVAILABLE {
			unavailable++
		}
	}
	require.GreaterOrEqual(t, unavailable, 2)
	floor, hash, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.Zero(t, floor)
	require.Empty(t, hash)
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)
	require.Zero(t, session.HeightSeedStatus().Seeded)
}

func TestFloorChat_MissingMapTimeIsNotReady(t *testing.T) {
	oracles, followers := sessionOracles(3, 100)
	followers[2].blind.Store(true)
	times := &chatGateTimes{ms: freshChatMs()}
	times.open.Store(true)
	session := openChatSession(t, oracles, times)

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.GreaterOrEqual(t, blockAcks(heightAcksInDiffs(seenDiffs(session))), 2)
	floor, _, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.NotZero(t, floor)
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)

	times.open.Store(false)
	beforeH, beforeA := times.gateCalls()
	st := session.HeightSeedStatus()
	require.Equal(t, HeightSeedStatePending, st.State)
	require.GreaterOrEqual(t, st.Seeded, 2)
	require.Equal(t, beforeH, times.headerCalls.Load())
	require.Equal(t, beforeA, times.awaitCalls.Load())
}

func TestFloorChat_HalfIntervalIsReady(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	now := time.Unix(1_700_000_000, 0).UTC()
	mapMs := now.Add(-interval / 2).UnixMilli()
	oracles, followers := sessionOracles(3, 100)
	followers[2].blind.Store(true)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, mapMs)
	session := openChatSession(t, oracles, times, clockedCadence(now, interval)...)

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.GreaterOrEqual(t, blockAcks(heightAcksInDiffs(seenDiffs(session))), 2)
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)

	rec := appliedStart(t, session)
	require.Equal(t, uint64(100), rec.StartedAtHeight)
	require.Equal(t, mapMs, rec.StartedAtTimeMs)
}

func TestFloorChat_OneIntervalPlusLagIsReady(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	now := time.Unix(1_700_000_000, 0).UTC()
	oracles, followers := sessionOracles(3, 100)
	followers[2].blind.Store(true)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, now.Add(-(interval + 3*time.Second)).UnixMilli())
	session := openChatSession(t, oracles, times, clockedCadence(now, interval)...)

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)
}

func TestFloorChat_StaleFloorSendsSpanThenCopiesFreshFloor(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0).UTC()}
	staleMs := clock.Now().Add(-(2*interval + time.Second)).UnixMilli()
	freshMs := clock.Now().Add(-interval / 2).UnixMilli()
	oracles, followers := sessionOracles(3, 100)
	followers[2].blind.Store(true)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, staleMs)
	times.set(200, freshMs)
	session := openChatSession(t, oracles, times,
		WithHeartbeatClock(clock.Now),
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: interval}))
	ctx := context.Background()

	require.NoError(t, session.MaybeHeartbeat(ctx))
	floor, _, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.Equal(t, uint64(100), floor)
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)
	base := session.Nonce()

	// The opening span just landed. The next one waits out the interval, then
	// copies F so the newer tip can raise it.
	clock.Advance(interval)
	followers[0].height.Store(200)
	followers[1].height.Store(200)
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.NoError(t, session.WaitFloorChat(waitCtx))

	var stamped int
	for _, d := range seenDiffs(session) {
		if d.Nonce <= base {
			continue
		}
		for _, tx := range d.Txs {
			hb := tx.GetHeartbeat()
			if hb == nil || hb.ObservedHeight == 0 {
				continue
			}
			stamped++
			require.Equal(t, uint64(100), hb.ObservedHeight)
			require.Equal(t, []byte{0xaa}, hb.ObservedBlockHash)
		}
	}
	require.NotZero(t, stamped)

	floor, hash, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.Equal(t, uint64(200), floor)
	require.Equal(t, []byte{0xaa}, hash)
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)

	rec := appliedStart(t, session)
	require.Equal(t, uint64(200), rec.StartedAtHeight)
	require.Equal(t, []byte{0xaa}, rec.StartedAtBlockHash)
	require.Equal(t, freshMs, rec.StartedAtTimeMs)
}

func TestFloorChat_HalfRosterIsBootstrapOnly(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	now := time.Unix(1_700_000_000, 0).UTC()
	oracles, followers := sessionOracles(3, 100)
	followers[2].blind.Store(true)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, now.Add(-interval/2).UnixMilli())
	session := openChatSession(t, oracles, times, clockedCadence(now, interval)...)

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)
	require.True(t, session.floorGateOpened.Load())

	// The retained-ack count has fallen below half. Bootstrap already opened,
	// and time(F) is still fresh, so chat stays open. The 30s cutoff does not
	// turn that drop into missed.
	session.floorGate.Store(&floorGateSnap{
		seeded: 0, slots: 3, floor: 100, hash: []byte{0xaa}, interval: interval,
	})
	session.MarkHeightSeedGate(time.Now().Add(-time.Second))
	st := session.HeightSeedStatus()
	require.Equal(t, HeightSeedStateOK, st.State)
	require.Zero(t, st.Seeded)
	require.NoError(t, session.WaitFloorChat(context.Background()))

	times.set(100, now.Add(-(2*interval + time.Second)).UnixMilli())
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)
}

func TestFloorChat_FreshOneOfThreeIsNotReady(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	now := time.Unix(1_700_000_000, 0).UTC()
	oracles, followers := sessionOracles(3, 100)
	followers[1].blind.Store(true)
	followers[2].blind.Store(true)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, now.Add(-interval/2).UnixMilli())
	session := openChatSession(t, oracles, times, clockedCadence(now, interval)...)

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.Equal(t, 1, blockAcks(heightAcksInDiffs(seenDiffs(session))))
	base := session.Nonce()
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)

	waitCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	err := session.WaitFloorChat(waitCtx)
	cancel()
	require.Error(t, err)
	require.Equal(t, base, session.Nonce())
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)
}

func TestFloorWarmupWaitsPastTheBootstrapDeadline(t *testing.T) {
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{ms: freshChatMs()}
	times.open.Store(true)
	session := openChatSession(t, oracles, times)
	session.MarkHeightSeedGate(time.Now().Add(-time.Second))
	require.Equal(t, HeightSeedStateMissed, session.HeightSeedStatus().State)

	errCh := make(chan error, 1)
	go func() {
		errCh <- session.WaitFloorWarmup(context.Background())
	}()
	select {
	case err := <-errCh:
		t.Fatalf("warmup returned while the gate was missed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, session.sendBootstrapSpan(context.Background()))
	session.floorGateNotify()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("warmup did not proceed after the gate opened")
	}
}

func TestFloorWarmupStopsWhenTheContextEnds(t *testing.T) {
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{ms: freshChatMs()}
	times.open.Store(true)
	session := openChatSession(t, oracles, times)
	session.MarkHeightSeedGate(time.Now().Add(-time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- session.WaitFloorWarmup(ctx)
	}()
	select {
	case err := <-errCh:
		t.Fatalf("warmup returned before cancel: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-errCh:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("warmup did not stop when its context ended")
	}
}

func TestFloorChat_BootstrapDeadlineMissesWhileFloorEmpty(t *testing.T) {
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{ms: freshChatMs()}
	times.open.Store(true)
	session := openChatSession(t, oracles, times)
	session.MarkHeightSeedGate(time.Now().Add(-time.Second))

	st := session.HeightSeedStatus()
	require.Equal(t, HeightSeedStateMissed, st.State)
	err := session.WaitFloorChat(context.Background())
	var se *HeightSeedError
	require.ErrorAs(t, err, &se)
	require.Equal(t, transport.DevshardErrorHeightSeedIncomplete, se.Code)
}

func TestFloorChat_StaleAfterBootstrapDeadlineRefreshes(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0).UTC()}
	staleMs := clock.Now().Add(-(2*interval + time.Second)).UnixMilli()
	freshMs := clock.Now().Add(-interval / 2).UnixMilli()
	oracles, followers := sessionOracles(3, 100)
	followers[2].blind.Store(true)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, staleMs)
	times.set(200, freshMs)
	session := openChatSession(t, oracles, times,
		WithHeartbeatClock(clock.Now),
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: interval}))
	ctx := context.Background()

	require.NoError(t, session.MaybeHeartbeat(ctx))
	session.MarkHeightSeedGate(time.Now().Add(-time.Second))
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)
	base := session.Nonce()

	clock.Advance(interval)
	followers[0].height.Store(200)
	followers[1].height.Store(200)
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.NoError(t, session.WaitFloorChat(waitCtx))
	require.Greater(t, session.Nonce(), base)
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)

	floor, _, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.Equal(t, uint64(200), floor)
}

func TestFloorChat_SecondRefreshWaitsOneInterval(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0).UTC()}
	oracles, followers := sessionOracles(3, 100)
	followers[2].blind.Store(true)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, clock.Now().Add(-(2*interval + time.Second)).UnixMilli())
	session := openChatSession(t, oracles, times,
		WithHeartbeatClock(clock.Now),
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: interval}))
	ctx := context.Background()

	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.False(t, session.heartbeat.TurnOpen())
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)
	base := session.Nonce()

	require.NoError(t, session.refreshFloorForChat(ctx))
	require.Equal(t, base, session.Nonce())

	clock.Advance(interval - time.Second)
	require.NoError(t, session.refreshFloorForChat(ctx))
	require.Equal(t, base, session.Nonce())

	clock.Advance(time.Second)
	require.NoError(t, session.refreshFloorForChat(ctx))
	require.Greater(t, session.Nonce(), base)
}

func TestFloorChat_WaiterWakesWhenMapTimeReturns(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	now := time.Unix(1_700_000_000, 0).UTC()
	oracles, followers := sessionOracles(3, 100)
	followers[2].blind.Store(true)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, now.Add(-interval/2).UnixMilli())
	session := openChatSession(t, oracles, times, clockedCadence(now, interval)...)

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)
	times.open.Store(false)
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)

	done := make(chan error, 1)
	go func() {
		done <- session.WaitFloorChat(context.Background())
	}()
	require.Eventually(t, func() bool {
		return session.floorGateWaiting.Load() == 1
	}, time.Second, time.Millisecond)

	times.open.Store(true)
	session.floorGateNotify()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not wake when the header time returned")
	}
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)
}

func TestFloorChat_LaterInferenceRequiresFreshFloor(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	now := time.Unix(1_700_000_000, 0).UTC()
	oracles, followers := sessionOracles(3, 100)
	followers[2].blind.Store(true)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, now.Add(-interval/2).UnixMilli())
	session := openChatSession(t, oracles, times, clockedCadence(now, interval)...)

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)
	_ = appliedStart(t, session)

	times.set(100, now.Add(-(2*interval + time.Second)).UnixMilli())
	session.MarkHeightSeedGate(time.Now().Add(-time.Second))
	base := session.Nonce()
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.Error(t, session.WaitFloorChat(ctx))
	require.Equal(t, base, session.Nonce())
}

func clockedCadence(now time.Time, interval time.Duration) []SessionOption {
	return []SessionOption{
		WithHeartbeatClock(func() time.Time { return now }),
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: interval}),
	}
}

func appliedStart(t *testing.T, session *Session) types.InferenceRecord {
	t.Helper()
	prepared, err := session.PrepareInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	var start *types.MsgStartInference
	for _, tx := range prepared.diff.Txs {
		if s := tx.GetStartInference(); s != nil {
			start = s
		}
	}
	require.NotNil(t, start)
	rec, ok := session.StateMachine().GetInference(prepared.Nonce())
	require.True(t, ok)
	require.Equal(t, start.ObservedHeight, rec.StartedAtHeight)
	require.Equal(t, start.ObservedBlockHash, rec.StartedAtBlockHash)
	return rec
}

func TestBlockAckParticipantCounts_CollapsesSlots(t *testing.T) {
	group := []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "A"},
		{SlotID: 1, ValidatorAddress: "A"},
		{SlotID: 2, ValidatorAddress: "A"},
		{SlotID: 3, ValidatorAddress: "B"},
		{SlotID: 4, ValidatorAddress: "C"},
	}
	acked, total := blockAckParticipantCounts(group, []uint32{0, 1, 2})
	require.Equal(t, 1, acked)
	require.Equal(t, 3, total)
	require.False(t, heightSeedQuorum(acked, total), "three slots of one participant are not half of three participants")

	acked, total = blockAckParticipantCounts(group, []uint32{2, 3})
	require.Equal(t, 2, acked)
	require.True(t, heightSeedQuorum(acked, total))

	acked, total = blockAckParticipantCounts(group, []uint32{1, 99})
	require.Equal(t, 1, acked)
}

// One participant that owns enough slots to pass a slot-half gate still leaves
// chat closed until half the addresses have a block ack.
func TestFloorChat_HalfOfParticipantsNotSlots(t *testing.T) {
	times := &chatGateTimes{ms: freshChatMs()}
	times.open.Store(true)
	session, followers := openMultiSlotChatSession(t, times)
	followers[3].blind.Store(true)
	followers[4].blind.Store(true)
	ctx := context.Background()

	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.GreaterOrEqual(t, session.StateMachine().HeightSyncSlotsWithBlockAck(), 3,
		"the multi-slot participant acked each of its slots")
	st := session.HeightSeedStatus()
	require.Equal(t, HeightSeedStatePending, st.State)
	require.Equal(t, 1, st.Seeded)
	require.Equal(t, 3, st.Slots)
	session.mu.Lock()
	ready := session.floorChatReadyLocked()
	session.mu.Unlock()
	require.False(t, ready)

	// One real block ack from the second address is the second participant.
	// Two of three opens the gate.
	followers[3].blind.Store(false)
	require.NoError(t, session.MaybeHeartbeat(ctx))
	st = session.HeightSeedStatus()
	require.Equal(t, HeightSeedStateOK, st.State)
	require.GreaterOrEqual(t, st.Seeded, 2)
	require.Equal(t, 3, st.Slots)
	session.mu.Lock()
	ready = session.floorChatReadyLocked()
	session.mu.Unlock()
	require.True(t, ready)
}

func openMultiSlotChatSession(t *testing.T, times *chatGateTimes) (*Session, []*sessionOracle) {
	t.Helper()
	signers := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	owners := []*signing.Secp256k1Signer{signers[0], signers[0], signers[0], signers[1], signers[2]}
	group := make([]types.SlotAssignment, len(owners))
	for i, signer := range owners {
		group[i] = types.SlotAssignment{SlotID: uint32(i), ValidatorAddress: signer.Address()}
	}
	oracles, followers := sessionOracles(len(group), 100)
	user := testutil.MustGenerateKey(t)
	config := testutil.DefaultConfig(len(group))
	verifier := signing.NewSecp256k1Verifier()
	smOpts := []state.SMOption{state.WithHeaderTimes(times)}
	clients := make([]HostClient, len(group))
	for i, signer := range owners {
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier, smOpts...)
		h, err := host.NewHost(sm, signer, stub.NewInferenceEngine(), "escrow-1", group, nil,
			host.WithGrace(100), host.WithChainOracle(oracles[i]))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier, smOpts...)
	var height uint64 = 100
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier,
		WithRequireHeightSeed(true),
		WithHeightSyncCadence(10, uint64(len(group))),
		WithObservedHeight(func() (uint64, []byte, bool) {
			return height, []byte{0xaa}, true
		}),
	)
	require.NoError(t, err)
	rememberJournal(session)
	session.MarkHeightSeedGate(time.Now().Add(heightSeedGate))
	t.Cleanup(func() { _ = session.Close() })
	return session, followers
}

func TestFloorChat_PrepareRejectsStaleFloorBeforeChooser(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, now.UnixMilli())
	opts := append(clockedCadence(now, heightsync.DefaultHeartbeatInterval), WithDisableHeightSyncHeartbeat(true), WithRequireHeightSeed(false))
	session := openChatSessionVersion(t, types.DevshardStateRootAndProtocolVersion, oracles, times, opts...)
	require.NoError(t, session.sendBootstrapSpan(context.Background()))
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)
	_ = appliedStart(t, session)
	times.set(100, now.Add(-time.Hour).UnixMilli())
	before := session.Nonce()
	called := false
	_, err := session.PrepareInferenceFn(func(HostBinding) (InferenceParams, bool, context.Context, error) {
		called = true
		return InferenceParams{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens}, false, context.Background(), nil
	})
	require.Error(t, err)
	require.False(t, called, "stale admission must not let the chooser consume a queued request")
	require.Equal(t, before, session.Nonce())
}

func TestFloorChat_MissingTimeBecomesStaleThenRefreshes(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0).UTC()}
	oracles, followers := sessionOracles(3, 100)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, clock.Now().UnixMilli())
	session := openChatSession(t, oracles, times, WithHeartbeatClock(clock.Now), WithDisableHeightSyncHeartbeat(true), WithRequireHeightSeed(false))
	require.NoError(t, session.sendBootstrapSpan(context.Background()))
	_ = appliedStart(t, session)
	clock.Advance(3 * interval)
	times.open.Store(false)
	times.set(200, clock.Now().UnixMilli())
	for _, follower := range followers {
		follower.height.Store(200)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.WaitFloorChat(ctx) }()
	require.Eventually(t, func() bool { return session.floorGateWaiting.Load() > 0 }, 500*time.Millisecond, time.Millisecond)
	times.open.Store(true)
	session.floorGateNotify()
	require.NoError(t, <-done)
	floor, _, known := session.sm.HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.Equal(t, uint64(200), floor)
}

func TestFloorChat_PostBootstrapWaitHasBudget(t *testing.T) {
	now := time.Now()
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, now.UnixMilli())
	session := openChatSession(t, oracles, times, WithDisableHeightSyncHeartbeat(true), WithRequireHeightSeed(false))
	require.NoError(t, session.sendBootstrapSpan(context.Background()))
	_ = appliedStart(t, session)
	times.open.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.WaitFloorChat(ctx) }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(WaitHeightSeedChatBudget + time.Second):
		cancel()
		<-done
		t.Fatal("post-bootstrap admission exceeded the floor wait budget")
	}
}

type floorRoundBlock struct {
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

type heldFloorClient struct {
	HostClient
	round *floorRoundBlock
}

func (c heldFloorClient) Send(ctx context.Context, req host.HostRequest, writer io.Writer, receipt func(*host.HostResponse)) (*host.HostResponse, error) {
	c.round.once.Do(func() { close(c.round.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.round.release:
	}
	return c.HostClient.Send(ctx, req, writer, receipt)
}

func TestFloorChat_CancelledCallerDoesNotCancelSharedRefresh(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0).UTC()}
	oracles, followers := sessionOracles(3, 100)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, clock.Now().UnixMilli())
	session := openChatSession(t, oracles, times, WithHeartbeatClock(clock.Now), WithDisableHeightSyncHeartbeat(true), WithRequireHeightSeed(false))
	require.NoError(t, session.sendBootstrapSpan(context.Background()))
	_ = appliedStart(t, session)
	clock.Advance(3 * interval)
	times.set(200, clock.Now().UnixMilli())
	for _, follower := range followers {
		follower.height.Store(200)
	}
	round := &floorRoundBlock{release: make(chan struct{}), started: make(chan struct{})}
	release := sync.OnceFunc(func() { close(round.release) })
	t.Cleanup(release)
	for i, client := range session.clients {
		session.clients[i] = heldFloorClient{HostClient: client, round: round}
	}
	before := session.Nonce()
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	first := make(chan error, 1)
	go func() { first <- session.WaitFloorChat(firstCtx) }()
	select {
	case <-round.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	secondCtx, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	second := make(chan error, 1)
	go func() { second <- session.WaitFloorChat(secondCtx) }()
	require.Eventually(t, func() bool { return session.floorGateWaiting.Load() >= 2 }, time.Second, time.Millisecond)
	firstCancel()
	require.Error(t, <-first)
	release()
	require.NoError(t, <-second)
	floor, _, known := session.sm.HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.Equal(t, uint64(200), floor)
	spans := 0
	for _, diff := range seenDiffs(session) {
		if diff.Nonce <= before {
			continue
		}
		for _, tx := range diff.Txs {
			if hb := tx.GetHeartbeat(); hb != nil && hb.ObservedHeight == 100 {
				spans++
			}
		}
	}
	require.Equal(t, len(session.group), spans, "concurrent callers must share one height span")
}

func TestFloorChat_SuccessfulExchangeWithoutFreshFloorDoesNotAdmit(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0).UTC()}
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, clock.Now().UnixMilli())
	session := openChatSession(t, oracles, times, WithHeartbeatClock(clock.Now), WithDisableHeightSyncHeartbeat(true), WithRequireHeightSeed(false))
	require.NoError(t, session.sendBootstrapSpan(context.Background()))
	_ = appliedStart(t, session)
	clock.Advance(3 * interval)
	before := session.Nonce()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.Error(t, session.WaitFloorChat(ctx))
	require.Equal(t, HeightSeedStatePending, session.HeightSeedStatus().State)
	for _, diff := range seenDiffs(session) {
		if diff.Nonce <= before {
			continue
		}
		for _, tx := range diff.Txs {
			require.Nil(t, tx.GetStartInference(), "a successful RPC is not floor readiness")
		}
	}
}

func TestFloorChat_BudgetSurvivesRepeatedWaits(t *testing.T) {
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{ms: freshChatMs()}
	times.open.Store(true)
	session := openChatSession(t, oracles, times, WithDisableHeightSyncHeartbeat(true), WithRequireHeightSeed(false))
	require.NoError(t, session.sendBootstrapSpan(context.Background()))
	ctx := session.WithFloorChatBudget(context.Background())
	deadline, ok := ctx.Value(floorChatBudgetKey{}).(time.Time)
	require.True(t, ok)
	require.Equal(t, deadline, session.WithFloorChatBudget(ctx).Value(floorChatBudgetKey{}))
}

func TestFloorChat_ExpiredCallerDoesNotStartRefresh(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0).UTC()}
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, clock.Now().UnixMilli())
	session := openChatSession(t, oracles, times, WithHeartbeatClock(clock.Now), WithDisableHeightSyncHeartbeat(true), WithRequireHeightSeed(false))
	require.NoError(t, session.sendBootstrapSpan(context.Background()))
	_ = appliedStart(t, session)
	clock.Advance(3 * interval)
	before := session.Nonce()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, session.WaitFloorChat(ctx))
	require.Equal(t, before, session.Nonce())
	session.floorRefreshMu.Lock()
	require.Nil(t, session.floorRefreshDone)
	session.floorRefreshMu.Unlock()
}

func TestFloorChat_CloseCancelsSharedRefresh(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0).UTC()}
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, clock.Now().UnixMilli())
	session := openChatSession(t, oracles, times, WithHeartbeatClock(clock.Now), WithDisableHeightSyncHeartbeat(true), WithRequireHeightSeed(false))
	require.NoError(t, session.sendBootstrapSpan(context.Background()))
	_ = appliedStart(t, session)
	clock.Advance(3 * interval)
	round := &floorRoundBlock{release: make(chan struct{}), started: make(chan struct{})}
	release := sync.OnceFunc(func() { close(round.release) })
	t.Cleanup(release)
	for i, client := range session.clients {
		session.clients[i] = heldFloorClient{HostClient: client, round: round}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiter := make(chan error, 1)
	go func() { waiter <- session.WaitFloorChat(ctx) }()
	select {
	case <-round.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- session.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel shared refresh")
	}
	require.Nil(t, session.startFloorRefresh(), "closed sessions do not start another operation")
	cancel()
	require.Error(t, <-waiter)
}

func TestFloorChat_RepeatedWaitUsesOriginalDeadline(t *testing.T) {
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{ms: freshChatMs()}
	times.open.Store(true)
	session := openChatSession(t, oracles, times, WithDisableHeightSyncHeartbeat(true), WithRequireHeightSeed(false))
	require.NoError(t, session.sendBootstrapSpan(context.Background()))
	ctx := session.WithFloorChatBudget(context.Background())
	require.NoError(t, session.WaitFloorChat(ctx))
	// Consume the actual admission budget, then retry with the same request.
	times.open.Store(false)
	require.Error(t, session.WaitFloorChat(ctx))
	started := time.Now()
	require.Error(t, session.WaitFloorChat(ctx))
	require.Less(t, time.Since(started), 250*time.Millisecond, "retry must not allocate another two seconds")
}
