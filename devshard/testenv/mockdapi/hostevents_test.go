package mockdapi

import (
	"context"
	"testing"
	"time"

	"common/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func escrowSub() []gen.HostEventKind {
	return []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
	}
}

func TestHostEvents_ImmediateAndCursorUnchanged(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring, nil, false)
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
	srv := newNodeManagerServer(nil, ring, nil, false)
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
	srv := newNodeManagerServer(nil, ring, nil, false)

	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, Generation: 999, Subscribe: escrowSub(),
	})
	require.NoError(t, err)
	require.True(t, resp.NeedsReset)
}

func TestHostEvents_SubscribeRequired(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring, nil, false)

	_, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{Cursor: 0})
	require.Error(t, err)
}

func blockSub() []gen.HostEventKind {
	return []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_BLOCK}
}

func allFeedSub() []gen.HostEventKind {
	return []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
	}
}

func appendBlock(ring *hostEventRing, height int64) uint64 {
	return ring.AppendBlock(&gen.BlockPayload{
		Height:           height,
		Hash:             []byte{byte(height)},
		HeaderTimeUnixMs: height * 1_000,
		EpochIndex:       1,
		Header:           []byte{0xa, byte(height)},
	})
}

func TestHostEvents_SupportedKinds(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring, nil, false)

	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, Subscribe: blockSub(),
	})
	require.NoError(t, err)
	require.ElementsMatch(t, mockSupportedKinds(), resp.SupportedKinds)
	require.NotContains(t, resp.SupportedKinds, gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_SCHEDULED)
	require.NotContains(t, resp.SupportedKinds, gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_CANCELED)
}

func TestHostEvents_AfterHeightTable(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring, nil, false)

	_, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, AfterHeight: 10, Subscribe: blockSub(),
	})
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))

	seq10 := appendBlock(ring, 10)
	seq12 := appendBlock(ring, 12)
	head := seq12

	at10, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, AfterHeight: 10, Subscribe: blockSub(),
	})
	require.NoError(t, err)
	require.False(t, at10.NeedsReset)
	require.Len(t, at10.Events, 1)
	require.Equal(t, int64(12), at10.Events[0].GetBlock().GetHeight())

	missing, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, AfterHeight: 11, Subscribe: blockSub(),
	})
	require.NoError(t, err)
	require.True(t, missing.NeedsReset)
	require.Empty(t, missing.Events)
	require.Equal(t, head, missing.NextCursor)

	atEmitted, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, AfterHeight: 12, Subscribe: blockSub(),
	})
	require.NoError(t, err)
	require.False(t, atEmitted.NeedsReset)
	require.Empty(t, atEmitted.Events)
	require.True(t, atEmitted.Unchanged)

	above, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, AfterHeight: 99, Subscribe: blockSub(),
	})
	require.NoError(t, err)
	require.False(t, above.NeedsReset)
	require.Empty(t, above.Events)
	require.Equal(t, head, above.NextCursor)

	beforeFirst, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, AfterHeight: 9, Subscribe: blockSub(),
	})
	require.NoError(t, err)
	require.False(t, beforeFirst.NeedsReset)
	require.Len(t, beforeFirst.Events, 2)
	require.Equal(t, int64(10), beforeFirst.Events[0].GetBlock().GetHeight())

	ignoreAfter, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: seq10, AfterHeight: 99, Subscribe: blockSub(),
	})
	require.NoError(t, err)
	require.False(t, ignoreAfter.NeedsReset)
	require.Len(t, ignoreAfter.Events, 1)
	require.Equal(t, int64(12), ignoreAfter.Events[0].GetBlock().GetHeight())
}

func TestHostEvents_PageLimit(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring, nil, false)
	for i := 1; i <= maxHostEventsPage+1; i++ {
		ring.AppendEscrowCreated(&gen.EscrowPayload{EscrowId: uint64(i)})
	}

	first, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, Subscribe: escrowSub(),
	})
	require.NoError(t, err)
	require.Len(t, first.Events, maxHostEventsPage)
	require.Equal(t, uint64(maxHostEventsPage), first.NextCursor)

	rest, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: first.NextCursor, Subscribe: escrowSub(),
	})
	require.NoError(t, err)
	require.Len(t, rest.Events, 1)
	require.Equal(t, uint64(maxHostEventsPage+1), rest.Events[0].GetEscrow().GetEscrowId())
	require.Equal(t, uint64(maxHostEventsPage)+1, rest.Events[0].Seq)
}

func TestHostEvents_StallSameGeneration(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring, nil, false)
	ring.AppendEscrowCreated(&gen.EscrowPayload{EscrowId: 1})

	before, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, Subscribe: escrowSub(),
	})
	require.NoError(t, err)
	genID := before.Generation

	ring.SetStalled(true)
	_, err = srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: before.NextCursor, Subscribe: escrowSub(),
	})
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))

	ring.SetStalled(false)
	after, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: before.NextCursor, Subscribe: escrowSub(),
	})
	require.NoError(t, err)
	require.Equal(t, genID, after.Generation)
	require.False(t, after.NeedsReset)
}

func TestHostEvents_GenerationBumpNeedsResetOnce(t *testing.T) {
	ring := newHostEventRing()
	srv := newNodeManagerServer(nil, ring, nil, false)
	appendBlock(ring, 10)
	appendBlock(ring, 11)

	reset, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, AfterHeight: 10, Subscribe: blockSub(),
	})
	require.NoError(t, err)
	require.False(t, reset.NeedsReset)

	ring.BumpGeneration()
	reset, err = srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: 0, AfterHeight: 10, Subscribe: blockSub(),
	})
	require.NoError(t, err)
	require.True(t, reset.NeedsReset)
	require.Empty(t, reset.Events)

	follow, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor: reset.NextCursor, AfterHeight: 0, Generation: reset.Generation, Subscribe: blockSub(),
	})
	require.NoError(t, err)
	require.False(t, follow.NeedsReset)
}

func TestHostEvents_AppendsWakeWaiter(t *testing.T) {
	cases := []struct {
		name   string
		append func(*hostEventRing)
		kind   gen.HostEventKind
	}{
		{"escrow", func(r *hostEventRing) { r.AppendEscrowCreated(&gen.EscrowPayload{EscrowId: 9}) }, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED},
		{"warm-key", func(r *hostEventRing) {
			r.AppendWarmKey(&gen.WarmKeyPayload{Granter: "g", Grantee: "w", GrantId: 1, Height: 4})
		}, gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED},
		{"block", func(r *hostEventRing) { appendBlock(r, 20) }, gen.HostEventKind_HOST_EVENT_KIND_BLOCK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ring := newHostEventRing()
			srv := newNodeManagerServer(nil, ring, nil, false)
			go func() {
				time.Sleep(80 * time.Millisecond)
				tc.append(ring)
			}()
			start := time.Now()
			resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
				Cursor: 0, MaxWaitSeconds: 10, Subscribe: allFeedSub(),
			})
			require.NoError(t, err)
			require.Less(t, time.Since(start), 5*time.Second)
			require.Len(t, resp.Events, 1)
			require.Equal(t, tc.kind, resp.Events[0].GetKind())
		})
	}
}
