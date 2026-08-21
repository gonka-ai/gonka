package inference

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
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

	prevEpochIndex, found := am.keeper.GetEffectiveEpochIndex(ctx)
	if !found {
		// No previous epoch (genesis bootstrap): leave CapWeight == Weight,
		// otherwise the entire initial validator set would be zeroed.
		am.LogInfo("Previous-epoch weight cap skipped: no effective epoch yet", types.PoC)
		return activeParticipants, nil
	}

	prevRoot, livePrevMembers, err := am.keeper.GetRootGroupDataWithLiveMembers(ctx)
	if err != nil {
		return nil, fmt.Errorf("load live previous-epoch members for trust cap: %w", err)
	}
	if prevRoot.EpochIndex != prevEpochIndex {
		return nil, fmt.Errorf(
			"trust-cap epoch mismatch: effective=%d group=%d",
			prevEpochIndex,
			prevRoot.EpochIndex,
		)
	}

	guardianAccounts := map[string]bool{}
	for _, opAddr := range am.keeper.GetGenesisGuardianAddresses(ctx) {
		accAddr, err := utils.OperatorAddressToAccAddress(opAddr)
		if err == nil {
			guardianAccounts[accAddr] = true
		}
	}

	capByAddress := am.buildPreviousConfirmedWeightCaps(ctx, prevEpochIndex, &prevRoot, livePrevMembers)

	newParticipantsZeroed := 0
	existingParticipantsClamped := 0
	for _, p := range activeParticipants {
		if p == nil {
			continue
		}
		if guardianAccounts[p.Index] {
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
// cap). Participants that were absent last epoch have CapWeight 0 and are dropped
// from the validator set entirely.
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

	guardianOperators := map[string]bool{}
	for _, opAddr := range am.keeper.GetGenesisGuardianAddresses(ctx) {
		guardianOperators[opAddr] = true
	}

	capped := make([]stakingkeeper.ComputeResult, 0, len(results))
	droppedNew := 0
	clamped := 0
	for _, r := range results {
		if guardianOperators[r.OperatorAddress] {
			capped = append(capped, r)
			continue
		}
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
		if r.Power <= 0 {
			// New/absent-last-epoch participant: no governance power this epoch.
			droppedNew++
			continue
		}
		capped = append(capped, r)
	}

	am.LogInfo("Previous-epoch weight cap applied to governance validators", types.EpochGroup,
		"epochIndex", epochIndex,
		"validatorsIn", len(results),
		"validatorsOut", len(capped),
		"clamped", clamped,
		"droppedNew", droppedNew)

	return capped
}

// buildPreviousConfirmedWeightCaps returns per-address confirmed effective
// weights for the previous epoch. The cap uses the trust policy: models without
// trusted cPoC voting power stay in the denominator but contribute zero to the
// next-epoch cap. Addresses present in the returned map were validators in the
// previous epoch; addresses absent from the map are treated as new participants
// by the caller.
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
			// Trust policy: untrusted models stay in the denominator but contribute
			// zero to next epoch's cap until trusted cPoC confirmation exists.
			capValue = types.EffectiveWeightFromModels(
				weight,
				scales,
				prevRoot.ConfirmationAccountingSeparated,
				nodesByAddress[vw.MemberAddress],
				vw.ConfirmationWeight,
				types.TrustWeightPolicy,
			)
		}
		caps[vw.MemberAddress] = capValue
	}
	return caps
}
