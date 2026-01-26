package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
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
	MissedRatio    float64
	// RewardedCoins from EpochPerformanceSummary (0 if punished, >0 if not)
	RewardedCoins uint64
}

// GetPunishmentStats computes whether a participant was punished in a given epoch
// by re-running the same statistical test used during settlement.
// This uses existing EpochPerformanceSummary data - no additional storage required.
func (k Keeper) GetPunishmentStats(ctx context.Context, epochIndex uint64, participantId string) (*PunishmentStats, error) {
	// Get the stored performance summary for this epoch/participant
	summary, found := k.GetEpochPerformanceSummary(ctx, epochIndex, participantId)
	if !found {
		return nil, status.Error(codes.NotFound, "participant not found for epoch")
	}

	total := summary.InferenceCount + summary.MissedRequests

	// Case 1: No work assigned - code returns full reward, not punishment
	// See accountsettle.go:53-54
	if total == 0 {
		return &PunishmentStats{
			WasPunished:    false,
			MissedRequests: 0,
			TotalRequests:  0,
			MissedRatio:    0,
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
	if err != nil {
		// Error in test = code returns full reward (not punishment)
		// See accountsettle.go:57-58
		return &PunishmentStats{
			WasPunished:    false,
			MissedRequests: summary.MissedRequests,
			TotalRequests:  total,
			MissedRatio:    float64(summary.MissedRequests) / float64(total),
			RewardedCoins:  summary.RewardedCoins,
		}, nil
	}

	wasPunished := !passed

	return &PunishmentStats{
		WasPunished:    wasPunished,
		MissedRequests: summary.MissedRequests,
		TotalRequests:  total,
		MissedRatio:    float64(summary.MissedRequests) / float64(total),
		RewardedCoins:  summary.RewardedCoins,
	}, nil
}

// GetPunishmentStatsForEpoch returns punishment stats for all participants in an epoch.
func (k Keeper) GetPunishmentStatsForEpoch(ctx context.Context, epochIndex uint64) ([]PunishmentStats, error) {
	all := k.GetAllEpochPerformanceSummary(ctx)

	var results []PunishmentStats
	for _, summary := range all {
		if summary.EpochIndex != epochIndex {
			continue
		}

		stats, err := k.GetPunishmentStats(ctx, epochIndex, summary.ParticipantId)
		if err != nil {
			continue
		}
		results = append(results, *stats)
	}

	return results, nil
}
