package keeper

import (
	"context"

	cosmossdk_math "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) MaintenanceCredit(ctx context.Context, req *types.QueryMaintenanceCreditRequest) (*types.QueryMaintenanceCreditResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	participantAddr, err := sdk.AccAddressFromBech32(req.Participant)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid participant address")
	}

	state, found := k.GetMaintenanceState(ctx, participantAddr)
	if !found {
		return &types.QueryMaintenanceCreditResponse{CreditBlocks: 0, Found: false}, nil
	}

	return &types.QueryMaintenanceCreditResponse{
		CreditBlocks: state.CreditBlocks,
		Found:        true,
	}, nil
}

func (k Keeper) MaintenanceScheduled(ctx context.Context, req *types.QueryMaintenanceScheduledRequest) (*types.QueryMaintenanceScheduledResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	participantAddr, err := sdk.AccAddressFromBech32(req.Participant)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid participant address")
	}

	state, found := k.GetMaintenanceState(ctx, participantAddr)
	if !found || state.ScheduledReservationId == 0 {
		return &types.QueryMaintenanceScheduledResponse{Found: false}, nil
	}

	reservation, found := k.GetMaintenanceReservation(ctx, state.ScheduledReservationId)
	if !found {
		return &types.QueryMaintenanceScheduledResponse{Found: false}, nil
	}

	return &types.QueryMaintenanceScheduledResponse{
		Reservation: &reservation,
		Found:       true,
	}, nil
}

func (k Keeper) MaintenanceActive(ctx context.Context, req *types.QueryMaintenanceActiveRequest) (*types.QueryMaintenanceActiveResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var activeReservations []*types.MaintenanceReservation

	// Iterate the active-reservation index instead of the full participant
	// state map. This is O(A) where A is the number of currently active
	// reservations (bounded by MaintenanceMaxConcurrentValidators) and
	// further capped by maxMaintenanceIterationLimit as a DoS safeguard.
	if err := k.iterateIndexedReservations(ctx, k.MaintenanceActiveIndex, func(r types.MaintenanceReservation) {
		if r.Status == types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE {
			r := r
			activeReservations = append(activeReservations, &r)
		}
	}); err != nil {
		return nil, status.Error(codes.Internal, "failed to iterate active maintenance index")
	}

	return &types.QueryMaintenanceActiveResponse{
		Reservations: activeReservations,
	}, nil
}

func (k Keeper) MaintenanceStatus(ctx context.Context, req *types.QueryMaintenanceStatusRequest) (*types.QueryMaintenanceStatusResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	participantAddr, err := sdk.AccAddressFromBech32(req.Participant)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid participant address")
	}

	state, found := k.GetMaintenanceState(ctx, participantAddr)
	if !found {
		return &types.QueryMaintenanceStatusResponse{Found: false}, nil
	}

	resp := &types.QueryMaintenanceStatusResponse{
		State: &state,
		Found: true,
	}

	if state.ActiveReservationId != 0 {
		r, found := k.GetMaintenanceReservation(ctx, state.ActiveReservationId)
		if found {
			resp.ActiveReservation = &r
		}
	}

	if state.ScheduledReservationId != 0 {
		r, found := k.GetMaintenanceReservation(ctx, state.ScheduledReservationId)
		if found {
			resp.ScheduledReservation = &r
		}
	}

	return resp, nil
}

func (k Keeper) MaintenanceConcurrency(ctx context.Context, req *types.QueryMaintenanceConcurrencyRequest) (*types.QueryMaintenanceConcurrencyResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	mp := k.GetMaintenanceParams(ctx)
	if mp == nil {
		return &types.QueryMaintenanceConcurrencyResponse{}, nil
	}

	targetHeight := req.Height
	if targetHeight == 0 {
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		targetHeight = sdkCtx.BlockHeight()
	}

	// Iterate the bounded set of ACTIVE + SCHEDULED reservations to find
	// those covering targetHeight.
	reservations, err := k.collectActiveAndScheduledReservations(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to collect maintenance reservations")
	}

	concurrentCount := uint32(0)
	concurrentPower := cosmossdk_math.ZeroInt()

	// Per-query power cache: a participant may appear with both a scheduled and
	// an active reservation that overlap targetHeight, and the bounded-but-
	// large iteration cap (maxMaintenanceIterationLimit) means an attacker who
	// stuffs the index could otherwise force up to 10000 staking lookups per
	// public query. Memoize per bech32 string so each distinct participant
	// costs exactly one staking lookup, regardless of reservation count.
	powerCache := make(map[string]cosmossdk_math.Int)
	seen := make(map[string]struct{})
	for _, r := range reservations {
		rEnd := r.StartHeight + int64(r.DurationBlocks) - 1
		if r.StartHeight > targetHeight || rEnd < targetHeight {
			continue
		}
		concurrentCount++
		// Only count each participant's power once even if they appear in
		// multiple overlapping reservations (e.g., scheduled + active edge).
		if _, dup := seen[r.Participant]; dup {
			continue
		}
		seen[r.Participant] = struct{}{}
		power, cached := powerCache[r.Participant]
		if !cached {
			rAddr, addrErr := sdk.AccAddressFromBech32(r.Participant)
			if addrErr != nil {
				powerCache[r.Participant] = cosmossdk_math.ZeroInt()
				continue
			}
			power = k.getParticipantPower(ctx, rAddr)
			powerCache[r.Participant] = power
		}
		concurrentPower = concurrentPower.Add(power)
	}

	// Express power as basis points of total. All integer math; clamp to int64
	// at the response boundary (bps fits comfortably in int64).
	totalPower := k.getTotalConsensusPower(ctx)
	var concurrentPowerBps int64
	if totalPower.IsPositive() {
		bps := concurrentPower.MulRaw(10000).Quo(totalPower)
		if bps.IsInt64() {
			concurrentPowerBps = bps.Int64()
		} else {
			concurrentPowerBps = 10000 // saturate
		}
	}

	return &types.QueryMaintenanceConcurrencyResponse{
		ConcurrentCount:    concurrentCount,
		ConcurrentPowerBps: concurrentPowerBps,
	}, nil
}

func (k Keeper) MaintenanceSchedulability(ctx context.Context, req *types.QueryMaintenanceSchedulabilityRequest) (*types.QueryMaintenanceSchedulabilityResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()

	reject := func(reason string) (*types.QueryMaintenanceSchedulabilityResponse, error) {
		return &types.QueryMaintenanceSchedulabilityResponse{Schedulable: false, RejectionReason: reason}, nil
	}

	mp := k.GetMaintenanceParams(ctx)
	if mp == nil || !mp.MaintenanceEnabled {
		return reject("maintenance windows are disabled")
	}

	participantAddr, err := sdk.AccAddressFromBech32(req.Participant)
	if err != nil {
		return reject("invalid participant address")
	}

	_, err = k.Participants.Get(ctx, participantAddr)
	if err != nil {
		return reject("participant not found")
	}

	if req.DurationBlocks == 0 {
		return reject("duration_blocks must be positive")
	}
	if req.DurationBlocks > mp.MaintenanceMaxWindowBlocks {
		return reject("duration exceeds maximum maintenance window blocks")
	}

	// Mirror the strict less-than check in ScheduleMaintenance: a request scheduled
	// exactly MinScheduleLeadBlocks blocks ahead is accepted.
	if req.StartHeight < blockHeight+int64(mp.MaintenanceMinScheduleLeadBlocks) {
		return reject("start height does not satisfy minimum scheduling lead time")
	}

	state := k.GetOrCreateMaintenanceState(ctx, participantAddr)
	if state.ScheduledReservationId != 0 {
		return reject("participant already has a scheduled maintenance window")
	}

	if state.CreditBlocks < req.DurationBlocks {
		return reject("insufficient maintenance credit")
	}

	if err := k.checkEpochPhaseOverlap(ctx, req.StartHeight, req.DurationBlocks, mp); err != nil {
		return reject(err.Error())
	}

	if err := k.checkConcurrencyLimits(ctx, req.StartHeight, req.DurationBlocks, participantAddr, mp); err != nil {
		return reject(err.Error())
	}

	if err := k.checkParticipantOverlap(ctx, participantAddr, req.StartHeight, req.DurationBlocks); err != nil {
		return reject(err.Error())
	}

	return &types.QueryMaintenanceSchedulabilityResponse{Schedulable: true}, nil
}
