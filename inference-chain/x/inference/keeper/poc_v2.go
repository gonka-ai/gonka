package keeper

import (
	"context"
	"errors"
	"math"
	"sort"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
)

func pocValidationV2Key(pocStageStartBlockHeight int64, participantAddr sdk.AccAddress, modelID string, validatorAddr sdk.AccAddress) collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]] {
	return collections.Join3(pocStageStartBlockHeight, participantAddr, collections.Join(modelID, validatorAddr))
}

func pocV2StoreCommitKey(pocStageStartBlockHeight int64, participantAddr sdk.AccAddress, modelID string) collections.Triple[int64, sdk.AccAddress, string] {
	return collections.Join3(pocStageStartBlockHeight, participantAddr, modelID)
}

// HasPocValidationV2 checks if a PoC v2 validation exists for the given key. Returns error on invalid addresses.
func (k Keeper) HasPocValidationV2(ctx context.Context, pocStageStartBlockHeight int64, participantAddress, modelID, validatorAddress string) (bool, error) {
	participantAddr, err := sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return false, err
	}
	validatorAddr, err := sdk.AccAddressFromBech32(validatorAddress)
	if err != nil {
		return false, err
	}
	return k.PoCValidationsV2.Has(ctx, pocValidationV2Key(pocStageStartBlockHeight, participantAddr, modelID, validatorAddr))
}

// CountPocValidationsV2ForValidator reads the (stage, validator) counter.
// The validation key ends with the validator, so this is not a prefix walk.
func (k Keeper) CountPocValidationsV2ForValidator(ctx context.Context, pocStageStartBlockHeight int64, validator sdk.AccAddress) (int, error) {
	n, err := k.PoCValidationV2Counts.Get(ctx, collections.Join(pocStageStartBlockHeight, validator))
	if errors.Is(err, collections.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if n > uint64(math.MaxInt) {
		return 0, sdkerrors.Wrap(types.ErrIllegalState, "validation count overflows int")
	}
	return int(n), nil
}

func (k Keeper) adjustPocValidationV2Count(ctx context.Context, stage int64, validator sdk.AccAddress, delta int) error {
	key := collections.Join(stage, validator)
	current, err := k.PoCValidationV2Counts.Get(ctx, key)
	if errors.Is(err, collections.ErrNotFound) {
		current = 0
		err = nil
	}
	if err != nil {
		return err
	}
	if delta < 0 {
		dec := uint64(-delta)
		if current <= dec {
			if current == 0 {
				return nil
			}
			return k.PoCValidationV2Counts.Remove(ctx, key)
		}
		current -= dec
		return k.PoCValidationV2Counts.Set(ctx, key, current)
	}
	next := current + uint64(delta)
	if next < current {
		return sdkerrors.Wrap(types.ErrIllegalState, "validation count overflow")
	}
	return k.PoCValidationV2Counts.Set(ctx, key, next)
}

// RebuildPocValidationV2Counts replaces signer counters for stages that can
// still accept votes. The v0.2.17 upgrade calls this once. Finished
// confirmation triggers are not read: those prefixes are attacker-sized and
// the bounded confirmation cleanup removes them later.
func (k Keeper) RebuildPocValidationV2Counts(ctx context.Context) error {
	existing, err := k.PoCValidationV2Counts.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	defer existing.Close()
	stale, err := existing.Keys()
	if err != nil {
		return err
	}
	sort.Slice(stale, func(i, j int) bool {
		if stale[i].K1() != stale[j].K1() {
			return stale[i].K1() < stale[j].K1()
		}
		return stale[i].K2().String() < stale[j].K2().String()
	})
	for _, key := range stale {
		if err := k.PoCValidationV2Counts.Remove(ctx, key); err != nil {
			return err
		}
	}

	stages, err := k.validationStagesStillAcceptingVotes(ctx)
	if err != nil {
		return err
	}
	for _, stage := range stages {
		if err := k.rebuildPocValidationV2CountsForStage(ctx, stage); err != nil {
			return err
		}
	}
	return nil
}

func (k Keeper) rebuildPocValidationV2CountsForStage(ctx context.Context, stage int64) error {
	iter, err := k.PoCValidationsV2.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]](stage))
	if err != nil {
		return err
	}
	defer iter.Close()
	totals := make(map[string]uint64)
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return err
		}
		totals[string([]byte(key.K3().K2()))]++
	}
	validators := make([]string, 0, len(totals))
	for validator := range totals {
		validators = append(validators, validator)
	}
	sort.Strings(validators)
	for _, validator := range validators {
		if err := k.PoCValidationV2Counts.Set(ctx, collections.Join(stage, sdk.AccAddress([]byte(validator))), totals[validator]); err != nil {
			return err
		}
	}
	return nil
}

// validationStagesStillAcceptingVotes returns the regular epoch stage and the
// active confirmation trigger when a vote submitted at this height would still
// be stored. Completed confirmation heights are omitted.
func (k Keeper) validationStagesStillAcceptingVotes(ctx context.Context) ([]int64, error) {
	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	if params.EpochParams == nil {
		return nil, nil
	}
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	stages := make(map[int64]struct{})
	if upcoming, found := k.GetUpcomingEpoch(ctx); found && upcoming != nil {
		ec := types.NewEpochContext(*upcoming, *params.EpochParams)
		if ec.IsValidationExchangeWindow(height) {
			stages[upcoming.PocStartBlockHeight] = struct{}{}
		}
	}
	active, isActive, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return nil, err
	}
	if isActive && active != nil &&
		active.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION &&
		active.IsInValidationWindow(height, params.EpochParams) {
		stages[active.TriggerHeight] = struct{}{}
	}
	ordered := make([]int64, 0, len(stages))
	for stage := range stages {
		ordered = append(ordered, stage)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered, nil
}

// HasPoCV2StoreCommit reports whether a store commit exists for the target
// participant and model at this stage.
func (k Keeper) HasPoCV2StoreCommit(ctx context.Context, pocStageStartBlockHeight int64, participantAddress, modelID string) (bool, error) {
	addr, err := sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return false, err
	}
	return k.PoCV2StoreCommits.Has(ctx, pocV2StoreCommitKey(pocStageStartBlockHeight, addr, modelID))
}

type PocStageVoterEligibility struct {
	eligibleModels map[string]bool
}

func (k Keeper) IsGenesisGuardianAccAddress(ctx context.Context, validatorAddr string) bool {
	if !k.GetGenesisGuardianEnabled(ctx) {
		return false
	}
	for _, opAddr := range k.GetGenesisGuardianAddresses(ctx) {
		accAddr, err := utils.OperatorAddressToAccAddress(opAddr)
		if err == nil && accAddr == validatorAddr {
			return true
		}
	}
	return false
}

func (k Keeper) BuildPocStageVoterEligibility(
	ctx context.Context,
	pocStageStartBlockHeight int64,
	validatorAddr string,
) (PocStageVoterEligibility, bool, error) {
	snapshot, found, err := k.GetPoCValidationSnapshot(ctx, pocStageStartBlockHeight)
	if err != nil || !found || len(snapshot.ModelVotingPowers) == 0 {
		return PocStageVoterEligibility{}, false, err
	}

	isGuardian := k.IsGenesisGuardianAccAddress(ctx, validatorAddr)
	eligibleModels := make(map[string]bool, len(snapshot.ModelVotingPowers))
	hasActiveModel := false
	for _, mvw := range snapshot.ModelVotingPowers {
		if mvw == nil || mvw.ModelId == "" || len(mvw.VotingPowers) == 0 {
			continue
		}
		hasActiveModel = true
		if isGuardian {
			eligibleModels[mvw.ModelId] = true
			continue
		}
		for _, e := range mvw.VotingPowers {
			if e != nil && e.Address == validatorAddr && e.VotingPower > 0 {
				eligibleModels[mvw.ModelId] = true
				break
			}
		}
	}
	if !hasActiveModel {
		return PocStageVoterEligibility{}, false, nil
	}

	return PocStageVoterEligibility{
		eligibleModels: eligibleModels,
	}, true, nil
}

// CanValidateModel returns true when the signer bound to this eligibility
// object has positive voting power for modelID in the stage snapshot, or is an
// enabled genesis guardian for a model present in the snapshot.
func (e PocStageVoterEligibility) CanValidateModel(modelID string) bool {
	return e.eligibleModels[modelID]
}

// SetPocValidationV2 stores a PoC v2 validation. Returns error on invalid addresses or storage failure.
func (k Keeper) SetPocValidationV2(ctx context.Context, validation types.PoCValidationV2) error {
	participantAddr, err := sdk.AccAddressFromBech32(validation.ParticipantAddress)
	if err != nil {
		return err
	}
	validatorAddr, err := sdk.AccAddressFromBech32(validation.ValidatorParticipantAddress)
	if err != nil {
		return err
	}
	pk := pocValidationV2Key(validation.PocStageStartBlockHeight, participantAddr, validation.ModelId, validatorAddr)
	exists, err := k.PoCValidationsV2.Has(ctx, pk)
	if err != nil {
		return err
	}
	k.LogInfo("PoC v2: Storing validation", types.PoC,
		"epoch", validation.PocStageStartBlockHeight,
		"participant", validation.ParticipantAddress,
		"model_id", validation.ModelId,
		"validator", validation.ValidatorParticipantAddress,
		"validated_weight", validation.ValidatedWeight)
	if err := k.PoCValidationsV2.Set(ctx, pk, validation); err != nil {
		return err
	}
	if exists {
		return nil
	}
	return k.adjustPocValidationV2Count(ctx, validation.PocStageStartBlockHeight, validatorAddr, 1)
}

// GetPoCValidationsV2ByStage collects all PoCValidationV2 grouped by participant and model for a specific epoch.
func (k Keeper) GetPoCValidationsV2ByStage(ctx context.Context, pocStageStartBlockHeight int64) (map[types.PoCParticipantModelKey][]types.PoCValidationV2, error) {
	result := make(map[types.PoCParticipantModelKey][]types.PoCValidationV2)

	iter, err := k.PoCValidationsV2.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]](pocStageStartBlockHeight))
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		validation, err := iter.Value()
		if err != nil {
			return nil, err
		}
		key := types.PoCParticipantModelKey{
			ParticipantAddress: validation.ParticipantAddress,
			ModelID:            validation.ModelId,
		}
		result[key] = append(result[key], validation)
	}

	return result, nil
}

// GetAllPoCV2StoreCommitsForStage returns all store commits for a given PoC stage, keyed by participant and model.
func (k Keeper) GetAllPoCV2StoreCommitsForStage(ctx context.Context, pocStageStartBlockHeight int64) (map[types.PoCParticipantModelKey]types.PoCV2StoreCommit, error) {
	result := make(map[types.PoCParticipantModelKey]types.PoCV2StoreCommit)

	iter, err := k.PoCV2StoreCommits.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](pocStageStartBlockHeight))
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return nil, err
		}
		value, err := iter.Value()
		if err != nil {
			return nil, err
		}
		addr := key.K2()
		result[types.PoCParticipantModelKey{
			ParticipantAddress: addr.String(),
			ModelID:            value.ModelId,
		}] = value
	}

	return result, nil
}

// GetAllMLNodeWeightDistributionsForStage returns all weight distributions for a given PoC stage, keyed by participant and model.
func (k Keeper) GetAllMLNodeWeightDistributionsForStage(ctx context.Context, pocStageStartBlockHeight int64) (map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution, error) {
	result := make(map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution)

	iter, err := k.MLNodeWeightDistributions.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](pocStageStartBlockHeight))
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return nil, err
		}
		value, err := iter.Value()
		if err != nil {
			return nil, err
		}
		addr := key.K2()
		result[types.PoCParticipantModelKey{
			ParticipantAddress: addr.String(),
			ModelID:            value.ModelId,
		}] = value
	}

	return result, nil
}

// GetMLNodeWeightDistribution reads one stored distribution. A missing key
// returns collections.ErrNotFound.
func (k Keeper) GetMLNodeWeightDistribution(ctx context.Context, pocStageStartBlockHeight int64, participantAddress, modelID string) (types.MLNodeWeightDistribution, error) {
	addr, err := sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return types.MLNodeWeightDistribution{}, err
	}
	return k.MLNodeWeightDistributions.Get(ctx, pocV2StoreCommitKey(pocStageStartBlockHeight, addr, modelID))
}

// SetPoCV2StoreCommit stores a PoCV2StoreCommit (for testing). Returns error on invalid address or storage failure.
func (k Keeper) SetPoCV2StoreCommit(ctx context.Context, commit types.PoCV2StoreCommit) error {
	addr, err := sdk.AccAddressFromBech32(commit.ParticipantAddress)
	if err != nil {
		return err
	}
	pk := pocV2StoreCommitKey(commit.PocStageStartBlockHeight, addr, commit.ModelId)
	return k.PoCV2StoreCommits.Set(ctx, pk, commit)
}

// SetMLNodeWeightDistribution stores an MLNodeWeightDistribution (for testing). Returns error on invalid address or storage failure.
func (k Keeper) SetMLNodeWeightDistribution(ctx context.Context, distribution types.MLNodeWeightDistribution) error {
	addr, err := sdk.AccAddressFromBech32(distribution.ParticipantAddress)
	if err != nil {
		return err
	}
	pk := pocV2StoreCommitKey(distribution.PocStageStartBlockHeight, addr, distribution.ModelId)
	return k.MLNodeWeightDistributions.Set(ctx, pk, distribution)
}
