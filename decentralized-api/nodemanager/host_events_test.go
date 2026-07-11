package nodemanager

import (
	"context"
	"testing"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"devshard/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testHostEventRing(t *testing.T) *apiconfig.HostEventRing {
	t.Helper()
	return apiconfig.NewHostEventRing(64, 1)
}

func escrowSubscribe() []gen.HostEventKind {
	return []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
	}
}

func TestNodeManager_GetHostEvents_CursorZeroReplaysRetained(t *testing.T) {
	ring := testHostEventRing(t)
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: 11}})
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowSettled, Escrow: &apiconfig.EscrowPayload{EscrowID: 11}})

	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         0,
		MaxWaitSeconds: 0,
		Subscribe:      escrowSubscribe(),
		Generation:     1,
	})
	require.NoError(t, err)
	require.False(t, resp.Unchanged)
	require.False(t, resp.NeedsReset)
	require.Equal(t, uint64(2), resp.NextCursor)
	require.Len(t, resp.Events, 2)
	require.Equal(t, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED, resp.Events[0].Kind)
	require.Equal(t, uint64(11), resp.Events[0].Escrow.EscrowId)
	require.Equal(t, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED, resp.Events[1].Kind)
}

func TestNodeManager_GetHostEvents_NilRingFailedPrecondition(t *testing.T) {
	srv := NewServer(&mockBroker{}, nil, nil)
	_, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe: escrowSubscribe(),
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestNodeManager_GetHostEvents_EmptySubscribeInvalidArgument(t *testing.T) {
	ring := testHostEventRing(t)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))
	_, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestNodeManager_GetHostEvents_ImmediateWhenBehind(t *testing.T) {
	ring := testHostEventRing(t)
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: 7}})
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindMaintenanceScheduled})
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowSettled, Escrow: &apiconfig.EscrowPayload{EscrowID: 7}})

	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))
	start := time.Now()
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         1,
		MaxWaitSeconds: 0,
		Subscribe:      escrowSubscribe(),
		Generation:     1,
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 50*time.Millisecond)
	require.False(t, resp.Unchanged)
	require.False(t, resp.NeedsReset)
	require.Equal(t, uint64(3), resp.NextCursor)
	require.Len(t, resp.Events, 1)
	require.Equal(t, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED, resp.Events[0].Kind)
	require.Equal(t, uint64(7), resp.Events[0].Escrow.EscrowId)
}

func TestNodeManager_GetHostEvents_LongPollWakesOnSubscribedKind(t *testing.T) {
	ring := testHostEventRing(t)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))

	done := make(chan *gen.GetHostEventsResponse, 1)
	go func() {
		resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
			Cursor:         0,
			MaxWaitSeconds: 2,
			Subscribe:      escrowSubscribe(),
			Generation:     1,
		})
		require.NoError(t, err)
		done <- resp
	}()

	time.Sleep(50 * time.Millisecond)
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: 3}})

	select {
	case resp := <-done:
		require.False(t, resp.Unchanged)
		require.Len(t, resp.Events, 1)
		require.Equal(t, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED, resp.Events[0].Kind)
		require.Equal(t, uint64(3), resp.Events[0].Escrow.EscrowId)
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("expected RPC to return on subscribed Append")
	}
}

func TestNodeManager_GetHostEvents_LongPollDoesNotWakeOnUnsubscribedKind(t *testing.T) {
	ring := testHostEventRing(t)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))

	done := make(chan *gen.GetHostEventsResponse, 1)
	go func() {
		resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
			Cursor:         0,
			MaxWaitSeconds: 1,
			Subscribe:      escrowSubscribe(),
			Generation:     1,
		})
		require.NoError(t, err)
		done <- resp
	}()

	time.Sleep(50 * time.Millisecond)
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindMaintenanceScheduled})

	select {
	case resp := <-done:
		require.True(t, resp.Unchanged, "unsubscribed Append must not return events")
		require.Empty(t, resp.Events)
		require.Equal(t, uint64(1), resp.NextCursor, "next_cursor still advances past skipped seq")
	case <-time.After(2 * time.Second):
		t.Fatal("expected timeout without subscribed wake")
	}
}

func TestNodeManager_GetHostEvents_RepeatedUnsubscribedAppendsStillTimeOut(t *testing.T) {
	ring := testHostEventRing(t)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))

	stop := make(chan struct{})
	defer close(stop)
	// Hammer maintenance (unsubscribed) appends throughout the wait window. With
	// a true wake filter these must not reset the escrow client's deadline.
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindMaintenanceScheduled})
			}
		}
	}()

	start := time.Now()
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         0,
		MaxWaitSeconds: 1,
		Subscribe:      escrowSubscribe(),
		Generation:     1,
	})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.True(t, resp.Unchanged, "escrow-only client must time out despite maintenance churn")
	require.Empty(t, resp.Events)
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
	require.Less(t, elapsed, 3*time.Second, "deadline must not be reset by unsubscribed appends")
}

func TestNodeManager_GetHostEvents_LongPollTimesOutUnchanged(t *testing.T) {
	ring := testHostEventRing(t)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))

	start := time.Now()
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         0,
		MaxWaitSeconds: 1,
		Subscribe:      escrowSubscribe(),
		Generation:     1,
	})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.True(t, resp.Unchanged)
	require.Empty(t, resp.Events)
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
	require.Less(t, elapsed, 3*time.Second)
}

func TestNodeManager_GetHostEvents_ContextCancel(t *testing.T) {
	ring := testHostEventRing(t)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := srv.GetHostEvents(ctx, &gen.GetHostEventsRequest{
			Cursor:         0,
			MaxWaitSeconds: 5,
			Subscribe:      escrowSubscribe(),
			Generation:     1,
		})
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(2 * time.Second):
		t.Fatal("expected cancel to unblock GetHostEvents")
	}
}

func TestNodeManager_GetHostEvents_GenerationReset(t *testing.T) {
	ring := testHostEventRing(t)
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))

	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         1,
		MaxWaitSeconds: 0,
		Subscribe:      escrowSubscribe(),
		Generation:     99, // stale boot nonce
	})
	require.NoError(t, err)
	require.True(t, resp.NeedsReset)
	require.Empty(t, resp.Events)
	require.Equal(t, uint64(1), resp.Generation)
	require.Equal(t, uint64(1), resp.NextCursor)
}

func TestNodeManager_GetHostEvents_GetRuntimeConfigStillWorks(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	ring := testHostEventRing(t)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1), WithHostEventRing(ring))

	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 50,
	})
	require.NoError(t, err)
	require.False(t, resp.Unchanged)
	require.Equal(t, int64(100), resp.Config.ParamsBlockHeight)
}

func TestNodeManager_GetHostEvents_ActiveLoadMap(t *testing.T) {
	ring := testHostEventRing(t)
	load := broker.NewEscrowLoadTracker(30 * time.Minute)
	now := time.Unix(1_700_000_000, 0)
	load.SetNowForTest(func() time.Time { return now })
	load.Record("7")
	load.Record("7")
	load.Record("8")

	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithEscrowLoadTracker(load))

	// Immediate path (no events): still attaches escrow_load.
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         0,
		MaxWaitSeconds: 0,
		Subscribe:      escrowSubscribe(),
		Generation:     1,
	})
	require.NoError(t, err)
	require.True(t, resp.Unchanged)
	require.Len(t, resp.EscrowLoad, 2)
	byID := map[uint64]float64{}
	for _, e := range resp.EscrowLoad {
		byID[e.EscrowId] = e.RequestsPerMin
	}
	require.InDelta(t, 2.0/30.0, byID[7], 1e-9)
	require.InDelta(t, 1.0/30.0, byID[8], 1e-9)

	// Idle escrows omitted after window expires.
	now = now.Add(31 * time.Minute)
	resp, err = srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         resp.NextCursor,
		MaxWaitSeconds: 0,
		Subscribe:      escrowSubscribe(),
		Generation:     1,
	})
	require.NoError(t, err)
	require.Empty(t, resp.EscrowLoad)

	// Timeout path also attaches a fresh snapshot.
	load.Record("9")
	resp, err = srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         resp.NextCursor,
		MaxWaitSeconds: 1,
		Subscribe:      escrowSubscribe(),
		Generation:     1,
	})
	require.NoError(t, err)
	require.True(t, resp.Unchanged)
	require.Len(t, resp.EscrowLoad, 1)
	require.Equal(t, uint64(9), resp.EscrowLoad[0].EscrowId)
}

func TestNodeManager_AcquireMLNode_RecordsEscrowLoad(t *testing.T) {
	load := broker.NewEscrowLoadTracker(time.Minute)
	srv := NewServer(&mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "lock-1", "http://n", "node-1", nil
		},
	}, nil, nil, WithEscrowLoadTracker(load))

	_, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{
		Model:    "m",
		EscrowId: "11",
	})
	require.NoError(t, err)
	snap := load.Snapshot()
	require.Len(t, snap, 1)
	require.Equal(t, uint64(11), snap[0].EscrowID)
}
