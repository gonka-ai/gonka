package inference

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

// applyPreviousConfirmedWeightCap computes each participant's CapWeight: the
// trust weight used for governance voting, BLS threshold signing and cPoC
// validation voting power. CapWeight is capped at the confirmed (cPoC-adjusted,
// coefficient-weighted) effective weight the participant held in the previous
// (current effective) epoch. Participants that were absent last epoch get
// CapWeight = 0, so a newly-appeared or returning participant must prove their
// compute for a full epoch before it counts toward consensus.
//
// Weight itself is left untouched: it remains the real weight used for rewards,
// cPoC confirmation and the next-epoch cap baseline. CapWeight defaults to Weight
// for every participant and is only lowered for capped/new participants; this
// invariant matters because governance, BLS and voting-power now read CapWeight,
// so it must always be populated.
//
// The cap value matches the settlement/reward "effective weight"
// (see keeper.calculateBitcoinRewards): weight * confirmed / rawConfirmationTotal,
// where confirmed and rawConfirmationTotal are aggregated from the per-model
// subgroups using the confirmation weight scale coefficients. This guarantees the
// cap uses the exact same coefficient-aware confirmed weight as rewards do.
//
// It runs at end-of-PoC-validation, after the universal 30% power cap and before
// per-model voting powers are computed, so the capped CapWeight flows to the
// staking/governance validator set (addEpochMembers -> SetComputeValidators), BLS
// slot assignment (InitiateBLSKeyGeneration) and cPoC validation voting power
// (computeAndSetVotingPowers).
func (am AppModule) applyPreviousConfirmedWeightCap(
	ctx context.Context,
	activeParticipants []*types.ActiveParticipant,
) ([]*types.ActiveParticipant, error) {
	// Default CapWeight to the real weight for everyone. Consumers read CapWeight,
	// so it must be populated even on the skip/bootstrap paths below.
	for _, p := range activeParticipants {
		if p != nil {
			p.CapWeight = p.Weight
		}
	}

	previous, err := am.getPreviousConfirmedWeights(ctx)
	if err != nil {
		return nil, err
	}
	if previous == nil {
		// No previous epoch (genesis bootstrap): leave CapWeight == Weight,
		// otherwise the entire initial validator set would be zeroed.
		am.LogInfo("Previous-epoch weight cap skipped: no effective epoch yet", types.PoC)
		return activeParticipants, nil
	}
	prevEpochIndex := previous.epochIndex
	capByAddress := previous.weights

	newParticipantsZeroed := 0
	existingParticipantsClamped := 0
	for _, p := range activeParticipants {
		if p == nil {
			continue
		}
		capValue, existed := capByAddress[p.Index]
		if !existed {
			// New (or returning after absence) participant: no confirmed weight
			// from the previous epoch, so they hold zero consensus weight until
			// they prove compute for a full epoch.
			if p.CapWeight != 0 {
				am.LogInfo("Previous-epoch weight cap: zeroing new participant", types.PoC,
					"participant", p.Index,
					"weight", p.Weight)
				p.CapWeight = 0
				newParticipantsZeroed++
			}
			continue
		}
		if p.CapWeight > capValue {
			am.LogInfo("Previous-epoch weight cap: clamping participant to previous confirmed weight", types.PoC,
				"participant", p.Index,
				"weight", p.Weight,
				"previousConfirmedWeight", capValue)
			p.CapWeight = capValue
			existingParticipantsClamped++
		}
	}

	am.LogInfo("Previous-epoch confirmed weight cap applied", types.PoC,
		"previousEpochIndex", prevEpochIndex,
		"participantCount", len(activeParticipants),
		"newParticipantsZeroed", newParticipantsZeroed,
		"existingParticipantsClamped", existingParticipantsClamped)

	return activeParticipants, nil
}

// applyTrustPowerCapping applies the universal concentration policy to the final
// trust vector. The earlier epoch power cap operates on real Weight, but lowering
// participants independently to their previous confirmed weights can concentrate
// the resulting CapWeight distribution again.
//
// Temporary participants keep the shared power-capping implementation isolated
// from real Weight. Only the capped values are copied back into CapWeight.
func (am AppModule) applyTrustPowerCapping(
	ctx context.Context,
	activeParticipants []*types.ActiveParticipant,
) {
	trustParticipants := make([]*types.ActiveParticipant, 0, len(activeParticipants))
	for _, participant := range activeParticipants {
		if participant == nil {
			continue
		}
		trustParticipants = append(trustParticipants, &types.ActiveParticipant{
			Index:  participant.Index,
			Weight: participant.CapWeight,
		})
	}

	result := ApplyPowerCapping(ctx, am.keeper, trustParticipants)
	cappedByAddress := make(map[string]int64, len(result.CappedParticipants))
	for _, participant := range result.CappedParticipants {
		if participant != nil {
			cappedByAddress[participant.Index] = participant.Weight
		}
	}
	for _, participant := range activeParticipants {
		if participant == nil {
			continue
		}
		participant.CapWeight = cappedByAddress[participant.Index]
	}

	if result.WasCapped {
		am.LogInfo("Universal power capping applied to trust weights", types.PoC,
			"cappedTotalPower", result.TotalPower,
			"participantCount", len(trustParticipants))
	}
}

// resolveTrustWeights returns the per-participant weight to use for consensus-
// facing operations (BLS threshold signing and cPoC validation voting power).
//
// When capApplied is true, CapWeight is used even if every value is zero. That
// distinguishes a post-upgrade epoch whose caps were computed and legitimately
// equal zero from a pre-upgrade epoch whose CapWeight field was never populated.
// If capApplied is false, a positive CapWeight still selects the cap path so
// partially-populated test fixtures keep working; otherwise it falls back to Weight.
func resolveTrustWeights(participants []*types.ActiveParticipant, capApplied bool) map[string]int64 {
	useCap := capApplied
	if !useCap {
		for _, p := range participants {
			if p != nil && p.CapWeight > 0 {
				useCap = true
				break
			}
		}
	}
	weights := make(map[string]int64, len(participants))
	for _, p := range participants {
		if p == nil {
			continue
		}
		if useCap {
			weights[p.Index] = p.CapWeight
		} else {
			weights[p.Index] = p.Weight
		}
	}
	return weights
}

// capComputeResultsToPreviousConfirmedWeight lowers each validator's governance
// power to the participant's CapWeight (their previous-epoch confirmed weight
// cap). Zero-power entries are retained here so guardian enhancement can raise a
// zero-cap guardian. Callers remove entries that remain non-positive afterward.
//
// Governance validator updates happen a couple of blocks after epoch formation,
// so this reads the persisted CapWeight from the epoch's stored
// ActiveParticipants rather than recomputing. The x/group member weights remain
// the real weight (so rewards, pricing and weighted selection are unaffected);
// only the CometBFT/governance power derived here is capped.
//
// For epochs formed before CapWeight existed (all CapWeight == 0), it returns the
// results unchanged so the upgrade transition does not zero the validator set.
func (am AppModule) capComputeResultsToPreviousConfirmedWeight(
	ctx context.Context,
	epochGroup *epochgroup.EpochGroup,
	results []stakingkeeper.ComputeResult,
) []stakingkeeper.ComputeResult {
	if epochGroup == nil || epochGroup.GroupData == nil {
		return results
	}
	epochIndex := epochGroup.GroupData.EpochIndex
	aps, found := am.keeper.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return results
	}

	capByAccount := make(map[string]int64, len(aps.Participants))
	anyCapSet := false
	for _, p := range aps.Participants {
		if p == nil {
			continue
		}
		capByAccount[p.Index] = p.CapWeight
		if p.CapWeight > 0 {
			anyCapSet = true
		}
	}
	if !aps.CapWeightApplied && !anyCapSet {
		// Epoch predates CapWeight (e.g. formed before the upgrade); do not cap.
		return results
	}

	capped := make([]stakingkeeper.ComputeResult, 0, len(results))
	clamped := 0
	for _, r := range results {
		valAddr, err := sdk.ValAddressFromBech32(r.OperatorAddress)
		if err != nil {
			capped = append(capped, r)
			continue
		}
		accAddr := sdk.AccAddress(valAddr).String()
		capValue, ok := capByAccount[accAddr]
		if !ok {
			// Not in this epoch's ActiveParticipants; leave untouched.
			capped = append(capped, r)
			continue
		}
		if r.Power > capValue {
			r.Power = capValue
			clamped++
		}
		capped = append(capped, r)
	}

	am.LogInfo("Previous-epoch weight cap applied to governance validators", types.EpochGroup,
		"epochIndex", epochIndex,
		"validatorsIn", len(results),
		"validatorsOut", len(capped),
		"clamped", clamped)

	return capped
}

// buildPreviousConfirmedWeightCaps returns per-address confirmed effective
// weights for the previous epoch. Addresses present in the returned map were
// validators in the previous epoch; addresses absent from the map are treated
// as new participants by the caller.
func (am AppModule) buildPreviousConfirmedWeightCaps(
	ctx context.Context,
	prevEpochIndex uint64,
	prevRoot *types.EpochGroupData,
	livePrevMembers map[string]bool,
) map[string]int64 {
	caps := make(map[string]int64, len(prevRoot.ValidationWeights))
	scales := prevRoot.ConfirmationWeightScales
	nodesByAddress := am.keeper.AggregateMLNodesFromModelSubgroups(ctx, prevEpochIndex, prevRoot.ValidationWeights)

	for _, vw := range prevRoot.ValidationWeights {
		if vw == nil || !livePrevMembers[vw.MemberAddress] {
			continue
		}
		weight := vw.Weight
		if weight < 0 {
			weight = 0
		}
		capValue := weight
		if len(scales) > 0 {
			rawConfirmationWeight := types.ConfirmationWeightOfModelNodes(
				nodesByAddress[vw.MemberAddress],
				scales,
			)
			capValue = types.EffectiveConfirmedWeight(
				weight,
				vw.ConfirmationWeight,
				rawConfirmationWeight,
			)
		}
		caps[vw.MemberAddress] = capValue
	}
	return caps
}

type previousConfirmedWeights struct {
	epochIndex  uint64
	weights     map[string]int64
	totalWeight int64
}

func (am AppModule) getPreviousConfirmedWeights(ctx context.Context) (*previousConfirmedWeights, error) {
	prevEpochIndex, found := am.keeper.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil, nil
	}

	prevRoot, livePrevMembers, err := am.keeper.GetRootGroupDataWithLiveMembers(ctx)
	if err != nil {
		return nil, fmt.Errorf("load live previous-epoch members for confirmed weights: %w", err)
	}
	if prevRoot.EpochIndex != prevEpochIndex {
		return nil, fmt.Errorf(
			"previous-confirmed-weight epoch mismatch: effective=%d group=%d",
			prevEpochIndex,
			prevRoot.EpochIndex,
		)
	}

	weights := am.buildPreviousConfirmedWeightCaps(ctx, prevEpochIndex, &prevRoot, livePrevMembers)
	totalWeight := int64(0)
	for _, weight := range weights {
		totalWeight += weight
	}
	return &previousConfirmedWeights{
		epochIndex:  prevEpochIndex,
		weights:     weights,
		totalWeight: totalWeight,
	}, nil
}
