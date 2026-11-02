package user

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/host"
)

// outageClient fails every send while down and counts the sends it sees.
type outageClient struct {
	inner HostClient
	down  atomic.Bool
	sends atomic.Int32
}

func (c *outageClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	c.sends.Add(1)
	if c.down.Load() {
		return nil, errors.New("host unreachable")
	}
	return c.inner.Send(ctx, req, stream, receiptHandler)
}

func withOutage(session *Session) []*outageClient {
	out := make([]*outageClient, len(session.clients))
	for i, c := range session.clients {
		out[i] = &outageClient{inner: c}
		out[i].down.Store(true)
		session.clients[i] = out[i]
	}
	return out
}

func setOutage(clients []*outageClient, down bool) {
	for _, c := range clients {
		c.down.Store(down)
	}
}

func totalSends(clients []*outageClient) int32 {
	var n int32
	for _, c := range clients {
		n += c.sends.Load()
	}
	return n
}

// steppedClock is a heartbeat clock the test advances by hand.
type steppedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func bootstrapFloor(t *testing.T, session *Session) uint64 {
	t.Helper()
	floor, _, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	return floor
}

func TestBootstrapRetry_ResendsUnackedHeartbeatsAfterInterval(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0).UTC()}
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, clock.Now().Add(-interval/2).UnixMilli())
	session := openChatSession(t, oracles, times,
		WithHeartbeatClock(clock.Now),
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: interval}))
	clients := withOutage(session)
	ctx := context.Background()

	require.NoError(t, session.sendBootstrapSpan(ctx))
	base := session.Nonce()
	require.Equal(t, 3, countHeartbeats(seenDiffs(session)))
	require.Zero(t, bootstrapFloor(t, session))
	rec := session.HeartbeatTurnTracker().Latest()
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnOpen, rec.State)
	turnStart := rec.TurnStart

	sends := totalSends(clients)
	clock.Advance(interval / 2)
	require.NoError(t, session.sendBootstrapSpan(ctx))
	require.Equal(t, base, session.Nonce())
	require.Equal(t, sends, totalSends(clients))

	setOutage(clients, false)
	clock.Advance(interval / 2)
	require.NoError(t, session.sendBootstrapSpan(ctx))

	require.Equal(t, 3, countHeartbeats(seenDiffs(session)))
	require.Equal(t, uint64(100), bootstrapFloor(t, session))
	require.GreaterOrEqual(t, session.StateMachine().HeightSyncSlotsWithBlockAck(), 2)
	rec = session.HeartbeatTurnTracker().Latest()
	require.Equal(t, turnStart, rec.TurnStart)
	times.set(100, clock.Now().Add(-interval/2).UnixMilli())
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)
}

func TestBootstrapRetry_RecoveredSessionResendsAtOnce(t *testing.T) {
	interval := heightsync.DefaultHeartbeatInterval
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0).UTC()}
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{}
	times.open.Store(true)
	times.set(100, clock.Now().Add(-interval/2).UnixMilli())
	session := openChatSession(t, oracles, times,
		WithHeartbeatClock(clock.Now),
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: interval}))
	clients := withOutage(session)
	ctx := context.Background()

	require.NoError(t, session.sendBootstrapSpan(ctx))
	require.Zero(t, bootstrapFloor(t, session))

	// A restart keeps the log's open bootstrap turn and loses the local
	// cadence state.
	session.mu.Lock()
	session.bootstrapSentAt = time.Time{}
	session.heartbeat.SettleTurn()
	session.mu.Unlock()

	setOutage(clients, false)
	require.NoError(t, session.sendBootstrapSpan(ctx))
	require.Equal(t, 3, countHeartbeats(seenDiffs(session)))
	require.Equal(t, uint64(100), bootstrapFloor(t, session))
	require.Equal(t, HeightSeedStateOK, session.HeightSeedStatus().State)
}

func TestBootstrapRetry_LoopRetriesUntilFloorSet(t *testing.T) {
	interval := 50 * time.Millisecond
	oracles, _ := sessionOracles(3, 100)
	times := &chatGateTimes{ms: freshChatMs()}
	times.open.Store(true)
	session := openChatSession(t, oracles, times,
		WithDisableHeightSyncHeartbeat(true),
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: interval}))
	clients := withOutage(session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.runBootstrapLoop(ctx)
	}()

	require.Eventually(t, func() bool { return totalSends(clients) >= 6 }, 5*time.Second, 5*time.Millisecond)
	require.Zero(t, bootstrapFloor(t, session))
	setOutage(clients, false)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap loop did not stop once F was set")
	}
	require.Equal(t, uint64(100), bootstrapFloor(t, session))
	require.Equal(t, 3, countHeartbeats(seenDiffs(session)))
}
