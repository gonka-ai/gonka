package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PunishmentStats contains computed punishment information for a participant in an epoch.
// Note: Uses current BinomTestP0 parameter - if governance changed p0 after settlement,
// results may differ from actual settlement outcome (edge case).
type PunishmentStats struct {
	WasPunished    bool
	MissedRequests uint64
	TotalRequests  uint64
	MissedRatio    decimal.Decimal
	// RewardedCoins from EpochPerformanceSummary (0 if punished, >0 if not)
	RewardedCoins uint64
}

// GetPunishmentStats computes whether a participant was punished in a given epoch
// by re-running the same statistical test used during settlement.
// This uses existing EpochPerformanceSummary data - no additional storage required.
func (k Keeper) GetPunishmentStats(ctx context.Context, epochIndex uint64, participantId string) (*PunishmentStats, error) {
	summary, found := k.GetEpochPerformanceSummary(ctx, epochIndex, participantId)
	if !found {
		return nil, status.Error(codes.NotFound, "participant not found for epoch")
	}

	return k.ComputePunishmentStats(ctx, summary)
}

// ComputePunishmentStats computes punishment stats from a summary.
// Exported so callers with existing summary data can avoid redundant lookups.
func (k Keeper) ComputePunishmentStats(ctx context.Context, summary types.EpochPerformanceSummary) (*PunishmentStats, error) {
	total := summary.InferenceCount + summary.MissedRequests

	// Case 1: No work assigned - code returns full reward, not punishment
	// See accountsettle.go:53-54
	if total == 0 {
		return &PunishmentStats{
			WasPunished:    false,
			MissedRequests: 0,
			TotalRequests:  0,
			MissedRatio:    decimal.Zero,
			RewardedCoins:  summary.RewardedCoins,
		}, nil
	}

	// Get p0 threshold from params (default 0.10)
	// See bitcoin_rewards.go:520-522
	p0 := types.DecimalFromFloat(0.10)
	params, err := k.GetParams(ctx)
	if err == nil && params.ValidationParams != nil && params.ValidationParams.BinomTestP0 != nil {
		p0 = params.ValidationParams.BinomTestP0
	}

	// Run the same statistical test used during settlement
	// See accountsettle.go:56
	passed, err := calculations.MissedStatTest(
		int(summary.MissedRequests),
		int(total),
		p0.ToDecimal(),
	)

	missedRatio := decimal.NewFromInt(int64(summary.MissedRequests)).Div(decimal.NewFromInt(int64(total)))

	if err != nil {
		// Error in test = code returns full reward (not punishment)
		// See accountsettle.go:57-58
		return &PunishmentStats{
			WasPunished:    false,
			MissedRequests: summary.MissedRequests,
			TotalRequests:  total,
			MissedRatio:    missedRatio,
			RewardedCoins:  summary.RewardedCoins,
		}, nil
	}

	wasPunished := !passed

	return &PunishmentStats{
		WasPunished:    wasPunished,
		MissedRequests: summary.MissedRequests,
		TotalRequests:  total,
		MissedRatio:    missedRatio,
		RewardedCoins:  summary.RewardedCoins,
	}, nil
}

// GetPunishmentStatsForEpoch returns punishment stats for all participants in an epoch.
// Uses ActiveParticipants (immutable) + batch lookup for efficient querying.
// Returns empty slice (not nil) when no results found.
func (k Keeper) GetPunishmentStatsForEpoch(ctx context.Context, epochIndex uint64) ([]PunishmentStats, error) {
	// Get ActiveParticipants which is immutable for the epoch
	activeParticipants, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return []PunishmentStats{}, nil
	}

	// Defensive nil check - see query_get_random_executor.go:123 for precedent
	if activeParticipants.Participants == nil {
		return []PunishmentStats{}, nil
	}

	// Extract participant IDs
	participantIds := make([]string, 0, len(activeParticipants.Participants))
	for _, ap := range activeParticipants.Participants {
		participantIds = append(participantIds, ap.Index)
	}

	if len(participantIds) == 0 {
		return []PunishmentStats{}, nil
	}

	// Batch lookup summaries for these participants
	summaries := k.GetParticipantsEpochSummaries(ctx, participantIds, epochIndex)

	// Compute punishment stats for each summary found
	results := make([]PunishmentStats, 0, len(summaries))
	for _, summary := range summaries {
		stats, err := k.ComputePunishmentStats(ctx, summary)
		if err != nil {
			continue
		}
		results = append(results, *stats)
	}

	return results, nil
}
