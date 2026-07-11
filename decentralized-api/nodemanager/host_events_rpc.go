package nodemanager

import (
	"context"
	"math"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/internal/longpoll"
	"decentralized-api/logging"
	"devshard/nodemanager/gen"

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

	// cursor 0 = live from now: do not replay retained events; wait for seq > head.
	cursor := req.GetCursor()
	if cursor == 0 {
		cursor = s.hostEvents.Head()
	}

	for {
		var wake <-chan struct{}
		if maxWait > 0 {
			// Subscribe before Since to avoid lost wake-ups.
			wake = s.hostEvents.NotifyChan()
		}

		got := s.hostEvents.Since(cursor, clientGen, subscribe)
		if got.Reset {
			logging.Info("host_events: GetHostEvents needs_reset", types.Config,
				"cursor", cursor,
				"clientGeneration", clientGen,
				"serverGeneration", got.Generation,
				"nextCursor", got.NextCursor,
			)
			return &gen.GetHostEventsResponse{
				Unchanged:  false,
				NextCursor: got.NextCursor,
				Generation: got.Generation,
				NeedsReset: true,
			}, nil
		}
		if len(got.Events) > 0 {
			logging.Debug("host_events: GetHostEvents returning events", types.Config,
				"cursor", cursor,
				"count", len(got.Events),
				"nextCursor", got.NextCursor,
				"generation", got.Generation,
			)
			return &gen.GetHostEventsResponse{
				Unchanged:  false,
				Events:     hostEventsToProto(got.Events),
				NextCursor: got.NextCursor,
				Generation: got.Generation,
			}, nil
		}

		if maxWait <= 0 {
			return &gen.GetHostEventsResponse{
				Unchanged:  true,
				NextCursor: got.NextCursor,
				Generation: got.Generation,
			}, nil
		}

		logging.Debug("host_events: GetHostEvents long-poll waiting", types.Config,
			"cursor", cursor,
			"nextCursor", got.NextCursor,
			"generation", got.Generation,
			"maxWait", maxWait,
		)
		outcome, err := longpoll.Wait(ctx, wake, maxWait)
		if err != nil {
			return nil, status.FromContextError(err).Err()
		}
		if outcome == longpoll.TimedOut {
			got = s.hostEvents.Since(cursor, clientGen, subscribe)
			logging.Debug("host_events: GetHostEvents long-poll timed out", types.Config,
				"cursor", cursor,
				"nextCursor", got.NextCursor,
				"generation", got.Generation,
				"maxWait", maxWait,
			)
			return &gen.GetHostEventsResponse{
				Unchanged:  true,
				NextCursor: got.NextCursor,
				Generation: got.Generation,
				NeedsReset: got.Reset,
			}, nil
		}
		// Notified: loop and re-check Since (unsubscribed kinds do not return yet).
	}
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
	return msg
}
