package simulation

import (
	"context"
	"errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// simValidatorPower is the uniform consensus power assigned to every
// sim validator. gonka sets DefaultPowerReduction to 1, so this is
// consensus power directly.
const simValidatorPower = 1_000_000

// EnsureModelsInEpochGroup promotes every model in the Models collection
// into the current epoch group's SubGroupModels. Sim-only bridge over
// the production gap that the PoC epoch flow normally closes: at sim
// InitGenesis the inference module writes models to k.Models but the
// genesis EpochGroup created by InitGenesisEpoch (module/genesis.go)
// has an empty SubGroupModels list. UpdateDynamicPricing
// (dynamic_pricing.go) iterates EpochGroup.SubGroupModels rather than
// Models, so without this seeding BeginBlocker never computes a price
// for the sim models and every StartInference fails with
// «current price not found for model» at RecordInferencePrice.
//
// Idempotent: CreateSubGroup checks in-memory cache and on-chain state
// before creating a new sub-group, so repeated calls are no-ops.
//
// Tolerance: if no current epoch group exists (unit-test contexts that
// don't run the full InitGenesisEpochGroup wiring), returns nil without
// touching state. Production sim app always has the genesis epoch group
// via InitGenesisEpoch, so this branch only fires in the keeper-mock
// tests in this package.
//
// No msg-server flow, no production semantics change.
func EnsureModelsInEpochGroup(ctx context.Context, k keeper.Keeper) error {
	eg, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		if errors.Is(err, types.ErrEffectiveEpochNotFound) || errors.Is(err, types.ErrEpochGroupDataNotFound) {
			return nil
		}
		return err
	}
	models, err := k.GetGovernanceModels(ctx)
	if err != nil {
		return err
	}
	for _, m := range models {
		if _, err := eg.CreateSubGroup(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// activeParticipantFromSimParticipant materializes an ActiveParticipant
// from a registered Participant + the simulation's fixed model list.
func activeParticipantFromSimParticipant(epoch uint64, participant types.Participant) *types.ActiveParticipant {
	models := append([]string(nil), SimModelIDs...)
	mlNodes := make([]*types.ModelMLNodes, 0, len(models))
	votingPowers := make([]*types.ModelVotingPower, 0, len(models))
	for _, modelID := range models {
		mlNodes = append(mlNodes, &types.ModelMLNodes{MlNodes: []*types.MLNodeInfo{{
			NodeId:     participant.Index + "/" + modelID,
			Throughput: simValidatorPower,
			PocWeight:  simValidatorPower,
		}}})
		votingPowers = append(votingPowers, &types.ModelVotingPower{
			ModelId:     modelID,
			VotingPower: simValidatorPower,
		})
	}
	return &types.ActiveParticipant{
		Index:        participant.Index,
		ValidatorKey: participant.ValidatorKey,
		Weight:       simValidatorPower,
		InferenceUrl: participant.InferenceUrl,
		Models:       models,
		Seed: &types.RandomSeed{
			Participant: participant.Index,
			EpochIndex:  epoch,
			Signature:   "sim-seed-" + participant.Index,
		},
		MlNodes:      mlNodes,
		VotingPowers: votingPowers,
	}
}

// EnsureSimActiveParticipantsSeeded promotes sim genesis participants
// into the current epoch group + writes ActiveParticipants + refreshes
// the transient validation cache and compute-validator set. Idempotent.
//
// Production code does this work at end of each PoC validation stage
// (module.go onEndOfPoCValidationStage → addEpochMembers + SetComputeValidators).
// At sim genesis the chain runs ~58 blocks before the first PoC stage
// completes; without this seeder those blocks have:
//   - no ActiveParticipants → participant-gated factories skip
//   - empty ValidationWeights → MsgValidation can't resolve sub-groups
//   - empty transient validation cache → pricing/validation BeginBlocker fails
//   - no compute validators → cometbft validator drain
//
// After the first real rotation populates ActiveParticipants[epoch+1] via
// the production pipeline, this helper is a no-op.
func EnsureSimActiveParticipantsSeeded(ctx context.Context, k keeper.Keeper) error {
	currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		return err
	}

	simParticipants := make([]types.Participant, 0, NumSimGenesisParticipants)
	iter, err := k.Participants.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	for ; iter.Valid(); iter.Next() {
		if len(simParticipants) >= NumSimGenesisParticipants {
			break
		}
		p, err := iter.Value()
		if err != nil {
			iter.Close()
			return err
		}
		simParticipants = append(simParticipants, p)
	}
	iter.Close()

	// Set RandomSeeds for upcoming epoch on every call. Production validators
	// submit MsgSubmitSeed; ComputeNewWeights reads RandomSeeds keyed by
	// (upcomingEpoch.Index, participantAddress) at end of PoC validation
	// — if missing, the commit is skipped. SetRandomSeed overwrites, so
	// per-call invocation is idempotent in effect.
	upcoming := currentEpoch + 1
	for _, p := range simParticipants {
		seed := types.RandomSeed{
			Participant: p.Index,
			EpochIndex:  upcoming,
			Signature:   "sim-seed-" + p.Index,
		}
		if err := k.SetRandomSeed(ctx, seed); err != nil {
			return err
		}
	}

	if _, found := k.GetActiveParticipants(ctx, currentEpoch); found {
		return nil
	}

	activeParticipants := make([]*types.ActiveParticipant, 0, len(simParticipants))
	for _, p := range simParticipants {
		activeParticipants = append(activeParticipants, activeParticipantFromSimParticipant(currentEpoch, p))
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()
	if err := k.SetActiveParticipants(ctx, types.ActiveParticipants{
		Participants:         activeParticipants,
		EpochGroupId:         currentEpoch,
		EpochId:              currentEpoch,
		PocStartBlockHeight:  blockHeight,
		EffectiveBlockHeight: blockHeight,
		CreatedAtBlockHeight: blockHeight,
	}); err != nil {
		return err
	}

	eg, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		if errors.Is(err, types.ErrEffectiveEpochNotFound) || errors.Is(err, types.ErrEpochGroupDataNotFound) {
			return nil
		}
		return err
	}
	for _, ap := range activeParticipants {
		member := epochgroup.NewEpochMemberFromActiveParticipant(ap, 0, 0, nil)
		if err := eg.AddMember(ctx, member); err != nil {
			return err
		}
	}

	if err := k.BuildEpochDataTransientCache(ctx); err != nil {
		return err
	}

	computeResult, err := eg.GetComputeResults(ctx)
	if err != nil {
		return err
	}
	if _, err := k.Staking.SetComputeValidators(ctx, computeResult, true); err != nil {
		return err
	}
	return nil
}
