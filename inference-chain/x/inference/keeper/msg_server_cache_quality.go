package keeper

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SubmitCacheQualitySummary handles MsgSubmitCacheQualitySummary.
//
// Security properties:
//   - Rejected if CacheQualityParams.Enabled is false (feature flag guard).
//   - Only the participant identified by Creator can submit their own summary.
//   - One summary per (epoch, participant) — duplicate submissions are rejected.
//   - CacheQualityWeight is computed server-side from governance params,
//     ignoring any client-supplied CacheQualityWeight field.
//   - CacheReuseCount and OriginalComputeCount must be non-negative.
//   - AvgSimilarityBps must be in [0, 10 000].
//
// Weight computation (bounded by governance):
//
//	rawWeight = CacheReuseCount × ReuseWeightCoefficient
//	cap       = StandardPoCNonces × MaxWeightFractionBps / 10 000
//	CacheQualityWeight = min(rawWeight, cap)
//
// Because StandardPoCNonces are not yet known at submission time (epoch is still
// open), we apply the cap lazily in calculateParticipantWeight instead, and store
// the raw pre-cap value here.  The keeper enforces an absolute ceiling of
// 10 000 nonce-equivalents to bound storage and impact.
func (k msgServer) SubmitCacheQualitySummary(goCtx context.Context, msg *types.MsgSubmitCacheQualitySummary) (*types.MsgSubmitCacheQualitySummaryResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ActiveParticipantPermission, PreviousActiveParticipantPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, fmt.Errorf("SubmitCacheQualitySummary: failed to get params: %w", err)
	}

	cqp := params.CacheQualityParams
	if cqp == nil || !cqp.Enabled {
		return nil, types.ErrCacheQualityDisabled
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	// CacheQualitySummaries are stored keyed by upcomingEpoch.Index (= effectiveEpoch+1)
	// because ComputeNewWeights loads them via:
	//   GetAllCacheQualityEpochSummariesForEpoch(ctx, upcomingEpoch.Index)
	// This is the same pattern as ContinuousPoCEpochSummaries — participants submit
	// with msg.EpochIndex = upcomingEpoch.Index (the epoch whose PoC they participated in).
	upcomingEpoch, found := k.GetUpcomingEpoch(ctx)
	if !found || upcomingEpoch == nil {
		return nil, fmt.Errorf("SubmitCacheQualitySummary: upcoming epoch not found")
	}
	if msg.Summary.EpochIndex != upcomingEpoch.Index {
		return nil, fmt.Errorf("SubmitCacheQualitySummary: summary epoch %d does not match upcoming epoch %d",
			msg.Summary.EpochIndex, upcomingEpoch.Index)
	}
	epochIndex := upcomingEpoch.Index

	// Reject duplicate: one summary per (epoch, participant).
	if _, exists := k.GetCacheQualityEpochSummary(ctx, epochIndex, msg.Creator); exists {
		return nil, types.ErrCacheQualitySummaryAlreadyExists
	}

	// Enforce embedding model version consistency.
	if msg.Summary.EmbeddingModelVersion != cqp.EmbeddingModelVersion {
		return nil, types.ErrCacheQualityModelVersionMismatch.Wrapf(
			"got %q, want %q", msg.Summary.EmbeddingModelVersion, cqp.EmbeddingModelVersion)
	}

	// Compute raw weight: CacheReuseCount × ReuseWeightCoefficient.
	// MaxWeightFractionBps cap is applied at settlement in calculateParticipantWeight
	// where the final PoC nonce count (commit.Count) is known.
	rawWeight := int64(0)
	if cqp.ReuseWeightCoefficient != nil {
		coeff := cqp.ReuseWeightCoefficient.ToDecimal()
		rawWeight = decimal.NewFromInt(msg.Summary.CacheReuseCount).Mul(coeff).IntPart()
	}
	const absoluteCap = int64(10_000)
	if rawWeight > absoluteCap {
		rawWeight = absoluteCap
	}

	summary := types.CacheQualityEpochSummary{
		ParticipantAddress:    msg.Creator,
		EpochIndex:            epochIndex,
		CacheReuseCount:       msg.Summary.CacheReuseCount,
		OriginalComputeCount:  msg.Summary.OriginalComputeCount,
		AvgSimilarityBps:      msg.Summary.AvgSimilarityBps,
		CacheQualityWeight:    rawWeight,
		EmbeddingModelVersion: cqp.EmbeddingModelVersion,
	}

	if err := k.SetCacheQualityEpochSummary(ctx, epochIndex, msg.Creator, summary); err != nil {
		return nil, fmt.Errorf("SubmitCacheQualitySummary: failed to store summary: %w", err)
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"cache_quality_summary_submitted",
		sdk.NewAttribute("participant", msg.Creator),
		sdk.NewAttribute("epoch", fmt.Sprintf("%d", epochIndex)),
		sdk.NewAttribute("cache_reuse_count", fmt.Sprintf("%d", summary.CacheReuseCount)),
		sdk.NewAttribute("cache_quality_weight", fmt.Sprintf("%d", summary.CacheQualityWeight)),
		sdk.NewAttribute("embedding_model_version", summary.EmbeddingModelVersion),
	))

	return &types.MsgSubmitCacheQualitySummaryResponse{}, nil
}
