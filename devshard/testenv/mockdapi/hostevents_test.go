package mockdapi

import (
	"context"
	"testing"
	"time"

	"common/nodemanager/gen"
	"github.com/stretchr/testify/require"
)

func escrowSub() []gen.HostEventKind {
	return []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
	}
}

func TestHostEvents_ImmediateAndCursorUnchanged(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring)
	ring.AppendEscrowCreated(&gen.EscrowPayload{EscrowId: 7})

	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, Subscribe: escrowSub(),
	})
	require.NoError(t, err)
	require.Len(t, resp.Events, 1)
	require.Equal(t, uint64(7), resp.Events[0].GetEscrow().GetEscrowId())
	require.Equal(t, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED, resp.Events[0].GetKind())
	require.Equal(t, uint64(1), resp.NextCursor)

	caughtUp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: resp.NextCursor, Subscribe: escrowSub(),
	})
	require.NoError(t, err)
	require.True(t, caughtUp.Unchanged)
	require.Empty(t, caughtUp.Events)
}

func TestHostEvents_LongPollWakesOnAppend(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring)
	go func() {
		time.Sleep(150 * time.Millisecond)
		ring.AppendEscrowCreated(&gen.EscrowPayload{EscrowId: 42})
	}()

	start := time.Now()
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, MaxWaitSeconds: 10, Subscribe: escrowSub(),
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 5*time.Second, "long-poll should wake promptly on append")
	require.Len(t, resp.Events, 1)
	require.Equal(t, uint64(42), resp.Events[0].GetEscrow().GetEscrowId())
}

func TestHostEvents_GenerationMismatchResets(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring)

	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, Generation: 999, Subscribe: escrowSub(),
	})
	require.NoError(t, err)
	require.True(t, resp.NeedsReset)
}

func TestHostEvents_SubscribeRequired(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring)

	_, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{Cursor: 0})
	require.Error(t, err)
}
