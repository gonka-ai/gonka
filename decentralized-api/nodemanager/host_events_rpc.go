package nodemanager

import (
	"context"
	"math"
	"time"

	"common/logging"
	"common/nodemanager/gen"
	"decentralized-api/apiconfig"
	"decentralized-api/internal/longpoll"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetHostEvents(ctx context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
	if s.hostEvents == nil {
		return nil, status.Error(codes.FailedPrecondition, "host events: ring not configured")
	}
	if len(req.GetSubscribe()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "host events: subscribe must be non-empty")
	}

	subscribe := hostEventKindsFromProto(req.GetSubscribe())
	maxWait := clampHostEventsMaxWait(req.GetMaxWaitSeconds())
	clientGen := req.GetGeneration()
	// cursor 0 = replay from the start of the retained window (bounded catch-up).
	cursor := req.GetCursor()

	for {
		resp, err, again := s.getHostEventsOnce(ctx, cursor, clientGen, subscribe, maxWait)
		if !again {
			return resp, err
		}
		// Notified by a subscribed kind: loop and re-check Since.
	}
}

// getHostEventsOnce runs one subscribe → Since → wait cycle. release is deferred
// so every exit path (return or loop-continue via again=true) deregisters the waiter.
func (s *Server) getHostEventsOnce(
	ctx context.Context,
	cursor, clientGen uint64,
	subscribe []apiconfig.HostEventKind,
	maxWait time.Duration,
) (resp *gen.GetHostEventsResponse, err error, again bool) {
	var wake <-chan struct{}
	var release func()
	if maxWait > 0 {
		// Subscribe (with the client's kind filter) before Since to avoid
		// lost wake-ups. Only subscribed kinds wake this waiter, so an
		// unsubscribed kind (e.g. maintenance) cannot reset the deadline.
		wake, release = s.hostEvents.Subscribe(subscribe)
	}
	if release != nil {
		defer release()
	}

	got := s.hostEvents.Since(cursor, clientGen, subscribe)
	if got.Reset {
		logging.Info("host_events: GetHostEvents needs_reset", types.Config,
			"cursor", cursor,
			"clientGeneration", clientGen,
			"serverGeneration", got.Generation,
			"nextCursor", got.NextCursor,
		)
		return s.hostEventsResponse(got, nil, false, true), nil, false
	}
	if len(got.Events) > 0 {
		logging.Debug("host_events: GetHostEvents returning events", types.Config,
			"cursor", cursor,
			"count", len(got.Events),
			"nextCursor", got.NextCursor,
			"generation", got.Generation,
		)
		return s.hostEventsResponse(got, hostEventsToProto(got.Events), false, false), nil, false
	}

	if maxWait <= 0 {
		return s.hostEventsResponse(got, nil, true, false), nil, false
	}

	logging.Debug("host_events: GetHostEvents long-poll waiting", types.Config,
		"cursor", cursor,
		"nextCursor", got.NextCursor,
		"generation", got.Generation,
		"maxWait", maxWait,
	)
	outcome, waitErr := longpoll.Wait(ctx, wake, maxWait)
	if waitErr != nil {
		return nil, status.FromContextError(waitErr).Err(), false
	}
	if outcome == longpoll.TimedOut {
		// The re-read advances next_cursor past anything appended between
		// the timeout and now, so what it finds has to travel in this
		// response: returned as "unchanged" it would be skipped for good.
		got = s.hostEvents.Since(cursor, clientGen, subscribe)
		logging.Debug("host_events: GetHostEvents long-poll timed out", types.Config,
			"cursor", cursor,
			"nextCursor", got.NextCursor,
			"generation", got.Generation,
			"maxWait", maxWait,
			"lateEvents", len(got.Events),
		)
		return s.hostEventsTimedOutResponse(got), nil, false
	}
	return nil, nil, true
}

// hostEventsTimedOutResponse is the answer to a long-poll that timed out: the
// re-read's events, if the ring got any between the deadline and the re-read,
// and "unchanged" only when it got none.
func (s *Server) hostEventsTimedOutResponse(got apiconfig.HostEventSince) *gen.GetHostEventsResponse {
	if got.Reset {
		return s.hostEventsResponse(got, nil, false, true)
	}
	return s.hostEventsResponse(got, hostEventsToProto(got.Events), len(got.Events) == 0, false)
}

func (s *Server) hostEventsResponse(got apiconfig.HostEventSince, events []*gen.HostEvent, unchanged, needsReset bool) *gen.GetHostEventsResponse {
	return &gen.GetHostEventsResponse{
		Unchanged:  unchanged,
		Events:     events,
		NextCursor: got.NextCursor,
		Generation: got.Generation,
		NeedsReset: needsReset,
		EscrowLoad: s.escrowLoadSnapshot(),
	}
}

func (s *Server) escrowLoadSnapshot() []*gen.EscrowLoad {
	if s.escrowLoad == nil {
		return nil
	}
	snap := s.escrowLoad.Snapshot()
	if len(snap) == 0 {
		return nil
	}
	out := make([]*gen.EscrowLoad, len(snap))
	for i, e := range snap {
		out[i] = &gen.EscrowLoad{
			EscrowId:       e.EscrowID,
			RequestsPerMin: e.RequestsPerMin,
		}
	}
	return out
}

func clampHostEventsMaxWait(maxWaitSeconds uint32) time.Duration {
	if maxWaitSeconds > math.MaxInt32 {
		return hostEventsMaxWaitCap()
	}
	return longpoll.ClampMaxWait(int32(maxWaitSeconds), hostEventsMaxWaitCap())
}

func hostEventKindsFromProto(in []gen.HostEventKind) []apiconfig.HostEventKind {
	out := make([]apiconfig.HostEventKind, len(in))
	for i, k := range in {
		out[i] = apiconfig.HostEventKind(k)
	}
	return out
}

func hostEventsToProto(in []apiconfig.HostEvent) []*gen.HostEvent {
	out := make([]*gen.HostEvent, len(in))
	for i, ev := range in {
		out[i] = hostEventToProto(ev)
	}
	return out
}

func hostEventToProto(ev apiconfig.HostEvent) *gen.HostEvent {
	msg := &gen.HostEvent{
		Seq:            ev.Seq,
		Kind:           gen.HostEventKind(ev.Kind),
		ObservedAtUnix: ev.ObservedAtUnix,
	}
	if ev.Escrow != nil {
		msg.Escrow = &gen.EscrowPayload{
			EscrowId:    ev.Escrow.EscrowID,
			EpochIndex:  ev.Escrow.EpochIndex,
			ModelId:     ev.Escrow.ModelID,
			Creator:     ev.Escrow.Creator,
			Amount:      ev.Escrow.Amount,
			Settler:     ev.Escrow.Settler,
			TotalPayout: ev.Escrow.TotalPayout,
			Fees:        ev.Escrow.Fees,
			Remainder:   ev.Escrow.Remainder,
		}
	}
	if ev.Maintenance != nil {
		msg.Maintenance = &gen.MaintenancePayload{
			ReservationId:  ev.Maintenance.ReservationID,
			Participant:    ev.Maintenance.Participant,
			StartHeight:    ev.Maintenance.StartHeight,
			DurationBlocks: ev.Maintenance.DurationBlocks,
			Reason:         ev.Maintenance.Reason,
		}
	}
	if ev.WarmKey != nil {
		msg.WarmKey = &gen.WarmKeyPayload{
			Granter:       ev.WarmKey.Granter,
			Grantee:       ev.WarmKey.Grantee,
			GrantId:       ev.WarmKey.GrantID,
			Height:        ev.WarmKey.Height,
			ExpiresAtUnix: ev.WarmKey.ExpiresAtUnix,
			End:           ev.WarmKey.End,
		}
	}
	return msg
}
