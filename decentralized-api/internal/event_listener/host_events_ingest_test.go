package event_listener

import (
	"errors"
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/nodemanager"
	"devshard/bridge"
	"devshard/nodemanager/gen"

	"github.com/stretchr/testify/require"
)

type stubEscrowQuerier struct {
	info *bridge.EscrowInfo
	err  error
}

func (s *stubEscrowQuerier) GetEscrow(escrowID string) (*bridge.EscrowInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.info == nil {
		return nil, bridge.ErrEscrowNotFound
	}
	out := *s.info
	out.EscrowID = escrowID
	return &out, nil
}

func testListenerWithRing(t *testing.T, opts ...EventListenerOption) (*EventListener, *apiconfig.HostEventRing) {
	t.Helper()
	ring := apiconfig.NewHostEventRing(64, 1)
	el := &EventListener{
		hostEvents: ring,
		eventHandlers: []EventHandler{
			&DevshardEscrowCreatedEventHandler{},
			&DevshardEscrowSettledEventHandler{},
			&MaintenanceScheduledEventHandler{},
			&MaintenanceCanceledEventHandler{},
		},
	}
	for _, opt := range opts {
		opt(el)
	}
	if el.hostEvents == nil {
		el.hostEvents = ring
	}
	return el, el.hostEvents
}

func txEvent(attrs map[string][]string) *chainevents.JSONRPCResponse {
	return &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Data:   chainevents.Data{Type: txEventType},
			Events: attrs,
		},
	}
}

func TestHostEvents_HasHandlerRecognizesNewEvents(t *testing.T) {
	el, _ := testListenerWithRing(t)
	require.True(t, el.hasHandler(txEvent(map[string][]string{
		"devshard_escrow_created.escrow_id": {"1"},
	})))
	require.True(t, el.hasHandler(txEvent(map[string][]string{
		"devshard_escrow_settled.escrow_id": {"1"},
	})))
	require.True(t, el.hasHandler(txEvent(map[string][]string{
		"maintenance_scheduled.reservation_id": {"9"},
	})))
	require.True(t, el.hasHandler(txEvent(map[string][]string{
		"maintenance_canceled.reservation_id": {"9"},
	})))
	require.False(t, el.hasHandler(txEvent(map[string][]string{
		"unrelated.event": {"1"},
	})))
}

func TestHostEvents_EscrowCreated_AppendsWhenInSlots(t *testing.T) {
	q := &stubEscrowQuerier{info: &bridge.EscrowInfo{Slots: []string{"host-a", "host-b"}}}
	el, ring := testListenerWithRing(t, WithEscrowQuerier(q), WithParticipantAddress("host-a"))

	err := (&DevshardEscrowCreatedEventHandler{}).Handle(txEvent(map[string][]string{
		"devshard_escrow_created.escrow_id":   {"42"},
		"devshard_escrow_created.epoch_index": {"3"},
		"devshard_escrow_created.model_id":    {"m1"},
		"devshard_escrow_created.creator":     {"creator"},
		"devshard_escrow_created.amount":      {"100"},
	}), el)
	require.NoError(t, err)

	got := ring.Since(0, 1, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	require.Len(t, got.Events, 1)
	require.Equal(t, uint64(42), got.Events[0].Escrow.EscrowID)
	require.Equal(t, "m1", got.Events[0].Escrow.ModelID)
}

func TestHostEvents_EscrowCreated_SkipsWhenNotInSlots(t *testing.T) {
	q := &stubEscrowQuerier{info: &bridge.EscrowInfo{Slots: []string{"other-host"}}}
	el, ring := testListenerWithRing(t, WithEscrowQuerier(q), WithParticipantAddress("host-a"))

	err := (&DevshardEscrowCreatedEventHandler{}).Handle(txEvent(map[string][]string{
		"devshard_escrow_created.escrow_id": {"42"},
	}), el)
	require.NoError(t, err)
	require.Equal(t, uint64(0), ring.Head())
}

func TestHostEvents_EscrowCreated_AppendsOnQueryFailure(t *testing.T) {
	q := &stubEscrowQuerier{err: errors.New("chain down")}
	el, ring := testListenerWithRing(t, WithEscrowQuerier(q), WithParticipantAddress("host-a"))

	err := (&DevshardEscrowCreatedEventHandler{}).Handle(txEvent(map[string][]string{
		"devshard_escrow_created.escrow_id": {"7"},
	}), el)
	require.NoError(t, err)
	require.Equal(t, uint64(1), ring.Head())
}

func TestHostEvents_EscrowSettled_Appends(t *testing.T) {
	q := &stubEscrowQuerier{info: &bridge.EscrowInfo{Slots: []string{"host-a"}}}
	el, ring := testListenerWithRing(t, WithEscrowQuerier(q), WithParticipantAddress("host-a"))

	err := (&DevshardEscrowSettledEventHandler{}).Handle(txEvent(map[string][]string{
		"devshard_escrow_settled.escrow_id":    {"7"},
		"devshard_escrow_settled.settler":      {"s"},
		"devshard_escrow_settled.total_payout": {"10"},
		"devshard_escrow_settled.fees":         {"1"},
		"devshard_escrow_settled.remainder":    {"0"},
	}), el)
	require.NoError(t, err)
	got := ring.Since(0, 1, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowSettled})
	require.Len(t, got.Events, 1)
	require.Equal(t, "10", got.Events[0].Escrow.TotalPayout)
}

func TestHostEvents_MaintenanceScheduledAndCanceled(t *testing.T) {
	el, ring := testListenerWithRing(t, WithParticipantAddress("host-a"))

	require.NoError(t, (&MaintenanceScheduledEventHandler{}).Handle(txEvent(map[string][]string{
		"maintenance_scheduled.reservation_id":  {"9"},
		"maintenance_scheduled.participant":     {"host-a"},
		"maintenance_scheduled.start_height":    {"100"},
		"maintenance_scheduled.duration_blocks": {"50"},
	}), el))

	require.NoError(t, (&MaintenanceCanceledEventHandler{}).Handle(txEvent(map[string][]string{
		"maintenance_canceled.reservation_id": {"9"},
		"maintenance_canceled.participant":    {"host-a"},
		"maintenance_canceled.credit_restored": {"50"},
	}), el))

	got := ring.Since(0, 1, []apiconfig.HostEventKind{
		apiconfig.HostEventKindMaintenanceScheduled,
		apiconfig.HostEventKindMaintenanceCanceled,
	})
	require.Len(t, got.Events, 2)
	require.Equal(t, uint64(9), got.Events[0].Maintenance.ReservationID)
	require.Equal(t, uint64(50), got.Events[0].Maintenance.DurationBlocks)
	require.Equal(t, apiconfig.HostEventKindMaintenanceCanceled, got.Events[1].Kind)
}

func TestHostEvents_MaintenanceSkipsOtherParticipant(t *testing.T) {
	el, ring := testListenerWithRing(t, WithParticipantAddress("host-a"))
	require.NoError(t, (&MaintenanceScheduledEventHandler{}).Handle(txEvent(map[string][]string{
		"maintenance_scheduled.reservation_id": {"1"},
		"maintenance_scheduled.participant":    {"someone-else"},
	}), el))
	require.Equal(t, uint64(0), ring.Head())
}

func TestHostEvents_LifecycleMaintenanceCanceled_NewBlock(t *testing.T) {
	el, ring := testListenerWithRing(t, WithParticipantAddress("host-a"))
	el.handleMaintenanceLifecycleEvents(&chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Data: chainevents.Data{Type: newBlockEventType},
			Events: map[string][]string{
				"maintenance_canceled.reservation_id":  {"11"},
				"maintenance_canceled.participant":     {"host-a"},
				"maintenance_canceled.credit_refunded": {"20"},
				"maintenance_canceled.reason":          {"maintenance_disabled"},
			},
		},
	}, "test")

	got := ring.Since(0, 1, []apiconfig.HostEventKind{apiconfig.HostEventKindMaintenanceCanceled})
	require.Len(t, got.Events, 1)
	require.Equal(t, "maintenance_disabled", got.Events[0].Maintenance.Reason)
	require.Equal(t, uint64(20), got.Events[0].Maintenance.DurationBlocks)
}

func TestHostEvents_UnhandledEventsStillDroppedByHasHandler(t *testing.T) {
	el, _ := testListenerWithRing(t)
	require.False(t, el.hasHandler(txEvent(map[string][]string{
		"some_other_module.foo": {"1"},
	})))
}

func TestHostEvents_IngestVisibleViaGetHostEvents(t *testing.T) {
	q := &stubEscrowQuerier{info: &bridge.EscrowInfo{Slots: []string{"host-a"}}}
	el, ring := testListenerWithRing(t, WithEscrowQuerier(q), WithParticipantAddress("host-a"))
	srv := nodemanager.NewServer(nil, nil, nil, nodemanager.WithHostEventRing(ring))

	require.NoError(t, (&DevshardEscrowCreatedEventHandler{}).Handle(txEvent(map[string][]string{
		"devshard_escrow_created.escrow_id": {"5"},
		"devshard_escrow_created.model_id":  {"m"},
	}), el))

	// Client caught up at head after first event; next create should appear.
	resp, err := srv.GetHostEvents(t.Context(), &gen.GetHostEventsRequest{
		Cursor:     ring.Head(),
		Subscribe:  []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED},
		Generation: 1,
	})
	require.NoError(t, err)
	require.True(t, resp.Unchanged)

	require.NoError(t, (&DevshardEscrowCreatedEventHandler{}).Handle(txEvent(map[string][]string{
		"devshard_escrow_created.escrow_id": {"6"},
	}), el))

	resp, err = srv.GetHostEvents(t.Context(), &gen.GetHostEventsRequest{
		Cursor:     1,
		Subscribe:  []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED},
		Generation: 1,
	})
	require.NoError(t, err)
	require.False(t, resp.Unchanged)
	require.Len(t, resp.Events, 1)
	require.Equal(t, uint64(6), resp.Events[0].Escrow.EscrowId)
}
