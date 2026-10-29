package event_listener

import (
	"context"
	"testing"
	"time"

	"common/nodemanager/gen"
	"decentralized-api/apiconfig"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/nodemanager"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
			&WarmKeyGrantedEventHandler{},
			&WarmKeyRevokedEventHandler{},
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

func TestHostEvents_ParsersReadPayloads(t *testing.T) {
	created, ok, err := parseEscrowCreated(map[string][]string{
		"devshard_escrow_created.escrow_id":   {"42"},
		"devshard_escrow_created.epoch_index": {"3"},
		"devshard_escrow_created.model_id":    {"m1"},
		"devshard_escrow_created.creator":     {"creator"},
		"devshard_escrow_created.amount":      {"100"},
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, apiconfig.HostEventKindEscrowCreated, created.Kind)
	require.Equal(t, uint64(42), created.Escrow.EscrowID)
	require.Equal(t, uint64(3), created.Escrow.EpochIndex)
	require.Equal(t, "m1", created.Escrow.ModelID)

	settled, ok, err := parseEscrowSettled(map[string][]string{
		"devshard_escrow_settled.escrow_id":    {"7"},
		"devshard_escrow_settled.settler":      {"s"},
		"devshard_escrow_settled.total_payout": {"10"},
		"devshard_escrow_settled.fees":         {"1"},
		"devshard_escrow_settled.remainder":    {"0"},
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "10", settled.Escrow.TotalPayout)

	scheduled, ok, err := parseMaintenanceScheduled(map[string][]string{
		"maintenance_scheduled.reservation_id":  {"9"},
		"maintenance_scheduled.participant":     {"host-a"},
		"maintenance_scheduled.start_height":    {"100"},
		"maintenance_scheduled.duration_blocks": {"50"},
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(9), scheduled.Maintenance.ReservationID)
	require.Equal(t, uint64(50), scheduled.Maintenance.DurationBlocks)

	canceled, ok, err := parseMaintenanceCanceled(map[string][]string{
		"maintenance_canceled.reservation_id":  {"11"},
		"maintenance_canceled.participant":     {"host-a"},
		"maintenance_canceled.credit_refunded": {"20"},
		"maintenance_canceled.reason":          {"maintenance_disabled"},
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "maintenance_disabled", canceled.Maintenance.Reason)
	require.Equal(t, uint64(20), canceled.Maintenance.DurationBlocks)
	require.True(t, skipForeignMaintenance("host-a", "someone-else"))
	require.False(t, skipForeignMaintenance("host-a", "host-a"))

	warm, err := parseWarmKeyEvents(map[string][]string{
		"warm_key_revoked.granter":    {"gonka1cold"},
		"warm_key_revoked.grantee":    {"gonka1old"},
		"warm_key_revoked.grant_id":   {"1"},
		"warm_key_revoked.height":     {"500"},
		"warm_key_revoked.end":        {"WARM_KEY_LOG_END_REPLACED"},
		"warm_key_granted.granter":    {"gonka1cold"},
		"warm_key_granted.grantee":    {"gonka1old"},
		"warm_key_granted.grant_id":   {"2"},
		"warm_key_granted.height":     {"500"},
		"warm_key_granted.expires_at": {"1700000000"},
	}, warmKeyGrantedEvent, apiconfig.HostEventKindWarmKeyGranted)
	require.NoError(t, err)
	require.Len(t, warm, 1)
	require.Equal(t, &apiconfig.WarmKeyPayload{Granter: "gonka1cold", Grantee: "gonka1old", GrantID: 2, Height: 500, ExpiresAtUnix: 1700000000}, warm[0].WarmKey)

	revoked, err := parseWarmKeyEvents(map[string][]string{
		"warm_key_revoked.granter":  {"gonka1cold"},
		"warm_key_revoked.grantee":  {"gonka1old"},
		"warm_key_revoked.grant_id": {"2"},
		"warm_key_revoked.height":   {"900"},
		"warm_key_revoked.end":      {"WARM_KEY_LOG_END_EXPIRED"},
	}, warmKeyRevokedEvent, apiconfig.HostEventKindWarmKeyRevoked)
	require.NoError(t, err)
	require.Equal(t, "WARM_KEY_LOG_END_EXPIRED", revoked[0].WarmKey.End)
	require.Equal(t, int64(900), revoked[0].WarmKey.Height)

	_, err = parseWarmKeyEvents(map[string][]string{
		"warm_key_granted.granter":  {"gonka1cold"},
		"warm_key_granted.grantee":  {"gonka1new"},
		"warm_key_granted.grant_id": {"x"},
		"warm_key_granted.height":   {"1"},
	}, warmKeyGrantedEvent, apiconfig.HostEventKindWarmKeyGranted)
	require.Error(t, err)
}

func TestHostEvents_HandlersAndNewBlockDoNotAppend(t *testing.T) {
	el, ring := testListenerWithRing(t, WithParticipantAddress("host-a"))
	el.configManager = &apiconfig.ConfigManager{}
	require.NoError(t, el.configManager.SetHeight(1000))

	events := []*chainevents.JSONRPCResponse{
		txEvent(map[string][]string{
			"tx.height":                         {"1"},
			"devshard_escrow_created.escrow_id": {"42"},
			"devshard_escrow_created.model_id":  {"m1"},
		}),
		txEvent(map[string][]string{
			"tx.height":                         {"1"},
			"devshard_escrow_settled.escrow_id": {"7"},
		}),
		txEvent(map[string][]string{
			"tx.height":                            {"1"},
			"maintenance_scheduled.reservation_id": {"9"},
			"maintenance_scheduled.participant":    {"host-a"},
		}),
		txEvent(map[string][]string{
			"tx.height":                           {"1"},
			"maintenance_canceled.reservation_id": {"9"},
			"maintenance_canceled.participant":    {"host-a"},
		}),
		txEvent(map[string][]string{
			"tx.height":                 {"1"},
			"warm_key_granted.grant_id": {"2"},
			"warm_key_granted.granter":  {"gonka1cold"},
			"warm_key_granted.grantee":  {"gonka1old"},
			"warm_key_granted.height":   {"500"},
		}),
		txEvent(map[string][]string{
			"tx.height":                 {"1"},
			"warm_key_revoked.grant_id": {"1"},
			"warm_key_revoked.granter":  {"gonka1cold"},
			"warm_key_revoked.grantee":  {"gonka1old"},
			"warm_key_revoked.height":   {"500"},
		}),
	}
	for _, ev := range events {
		el.processEvent(ev, "test")
		for _, h := range el.eventHandlers {
			if h.CanHandle(ev) {
				require.NoError(t, h.Handle(ev, el))
			}
		}
	}
	el.handleMaintenanceLifecycleEvents(&chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Data: chainevents.Data{Type: newBlockEventType},
			Events: map[string][]string{
				"maintenance_canceled.reservation_id": {"11"},
				"maintenance_canceled.participant":    {"host-a"},
			},
		},
	}, "test")
	el.handleWarmKeyExpiryEvents(&chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Data: chainevents.Data{Type: newBlockEventType},
			Events: map[string][]string{
				"warm_key_revoked.grant_id": {"2"},
				"warm_key_revoked.granter":  {"gonka1cold"},
				"warm_key_revoked.grantee":  {"gonka1old"},
				"warm_key_revoked.height":   {"900"},
			},
		},
	}, "test")
	require.NoError(t, (&WarmKeyGrantedEventHandler{}).Handle(txEvent(map[string][]string{
		"warm_key_granted.grant_id": {"x"},
		"warm_key_granted.granter":  {"gonka1cold"},
		"warm_key_granted.grantee":  {"gonka1new"},
		"warm_key_granted.height":   {"1"},
	}), el))

	require.Equal(t, uint64(0), ring.Head())
}

func TestHostEvents_IngestVisibleViaGetHostEvents(t *testing.T) {
	_, ring := testListenerWithRing(t)
	srv := nodemanager.NewServer(nil, nil, nil, nodemanager.WithHostEventRing(ring))

	ring.Append(apiconfig.HostEvent{
		Kind:   apiconfig.HostEventKindEscrowCreated,
		Escrow: &apiconfig.EscrowPayload{EscrowID: 5, ModelID: "m"},
	})

	resp, err := srv.GetHostEvents(t.Context(), &gen.GetHostEventsRequest{
		Cursor:     ring.Head(),
		Subscribe:  []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED},
		Generation: 1,
	})
	require.NoError(t, err)
	require.True(t, resp.Unchanged)

	ring.Append(apiconfig.HostEvent{
		Kind:   apiconfig.HostEventKindEscrowCreated,
		Escrow: &apiconfig.EscrowPayload{EscrowID: 6},
	})

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

func TestHostEventFeed_SyncStatusWithoutNewBlockStallsRPC(t *testing.T) {
	bo := NewBlockObserverWithClient(&apiconfig.ConfigManager{}, newMockTmHTTPClient(0))
	bo.currentBlockHeight.Store(5)
	feed := &HostEventFeed{bo: bo}
	feed.emitted.Store(5)
	el := &EventListener{blockObserver: bo}

	el.recordSyncHeights(1, 15)
	require.Equal(t, int64(15), feed.TipHeight())
	require.Equal(t, int64(1), bo.earliestBlock.Load())
	el.recordSyncHeights(1, 10)
	require.Equal(t, int64(15), feed.TipHeight())

	ring := apiconfig.NewHostEventRing(64, 1)
	srv := nodemanager.NewServer(nil, nil, nil,
		nodemanager.WithHostEventRing(ring),
		nodemanager.WithHostEventStatus(feed),
	)
	start := time.Now()
	_, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:      []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_BLOCK},
		Generation:     1,
		MaxWaitSeconds: 5,
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Contains(t, err.Error(), "height 6")
	require.Less(t, time.Since(start), time.Second)
}
