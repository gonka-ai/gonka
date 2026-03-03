package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// ExecutorMissStats tracks missed inference counts per executor for diagnostics.
type ExecutorMissStats struct {
	Address        string
	MissedRequests uint64
	InferenceCount uint64
}

// LogInferenceHealthSummary logs a periodic health summary of the inference system.
// Called from EndBlock at configurable intervals to help operators monitor inference health.
func (k Keeper) LogInferenceHealthSummary(ctx context.Context, blockHeight int64) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Only log every 100 blocks to avoid log spam
	if blockHeight%100 != 0 {
		return
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("InferenceHealthSummary: failed to get params", types.Inferences, "error", err)
		return
	}

	effectiveEpoch, found := k.GetEffectiveEpoch(ctx)
	if !found || effectiveEpoch == nil {
		return
	}

	// Count pending inference timeouts (inferences waiting to expire)
	allTimeouts := k.GetAllInferenceTimeout(ctx)
	pendingCount := 0
	for _, t := range allTimeouts {
		if t.ExpirationHeight > uint64(blockHeight) {
			pendingCount++
		}
	}

	// Get participant miss stats from current epoch group
	currentEpochGroup, err := k.GetEpochGroupForEpoch(ctx, *effectiveEpoch)
	if err != nil {
		k.LogError("InferenceHealthSummary: failed to get epoch group", types.Inferences, "error", err)
		return
	}

	activeCount := 0
	if currentEpochGroup.GroupData != nil {
		activeCount = len(currentEpochGroup.GroupData.ValidationWeights)
	}

	// Find executors with highest missed request counts this epoch
	topMissed := k.getTopMissedExecutors(ctx, currentEpochGroup.GroupData, 5)

	k.LogInfo("InferenceHealthSummary", types.Inferences,
		"blockHeight", blockHeight,
		"blockTime", sdkCtx.BlockTime().UTC().String(),
		"epochIndex", effectiveEpoch.Index,
		"pendingInferenceTimeouts", pendingCount,
		"totalTimeoutsInStore", len(allTimeouts),
		"activeParticipants", activeCount,
		"expirationBlocks", params.ValidationParams.ExpirationBlocks,
		"topMissedExecutors", formatMissedExecutors(topMissed))
}

// getTopMissedExecutors returns the top N executors by missed request count in the current epoch.
func (k Keeper) getTopMissedExecutors(ctx context.Context, groupData *types.EpochGroupData, limit int) []ExecutorMissStats {
	if groupData == nil {
		return nil
	}

	var stats []ExecutorMissStats
	for _, weight := range groupData.ValidationWeights {
		participant, found := k.GetParticipant(ctx, weight.MemberAddress)
		if !found {
			continue
		}
		if participant.CurrentEpochStats.MissedRequests > 0 {
			stats = append(stats, ExecutorMissStats{
				Address:        weight.MemberAddress,
				MissedRequests: participant.CurrentEpochStats.MissedRequests,
				InferenceCount: participant.CurrentEpochStats.InferenceCount,
			})
		}
	}

	// Sort by missed requests descending (simple insertion sort for small N)
	for i := 1; i < len(stats); i++ {
		j := i
		for j > 0 && stats[j].MissedRequests > stats[j-1].MissedRequests {
			stats[j], stats[j-1] = stats[j-1], stats[j]
			j--
		}
	}

	if len(stats) > limit {
		stats = stats[:limit]
	}

	return stats
}

func formatMissedExecutors(stats []ExecutorMissStats) string {
	if len(stats) == 0 {
		return "none"
	}
	result := ""
	for i, s := range stats {
		if i > 0 {
			result += "; "
		}
		result += fmt.Sprintf("%s(missed=%d,total=%d)", truncateAddress(s.Address), s.MissedRequests, s.InferenceCount)
	}
	return result
}

func truncateAddress(addr string) string {
	if len(addr) <= 20 {
		return addr
	}
	return addr[:10] + "..." + addr[len(addr)-6:]
}
