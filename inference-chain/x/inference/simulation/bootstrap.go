package simulation

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// simValidatorPower is the uniform consensus power EnsureComputeValidators
// assigns to every validator on a refresh. gonka sets DefaultPowerReduction to
// 1, so this is consensus power directly. The exact value is irrelevant — only
// that it is positive and uniform (keeps the refresh deterministic).
const simValidatorPower = 1_000_000

// simBondedValidatorFloor is the bonded-validator count below which
// EnsureComputeValidators refreshes the set. Kept well above zero so the
// cometbft validator set cannot empty between two factory invocations even
// across a stretch of non-x/inference blocks (downtime jailing was observed at
// ~1.7 validators/block; this floor gives >25 blocks of grace).
const simBondedValidatorFloor = 50

// EnsureMembersInEpochGroup writes a sim-only ValidationWeights entry
// for each ActiveParticipant into every sub-group's EpochGroupData. Used
// by MsgValidationFactory so the handler's transient-cache lookup at
// GetCachedEpochDataModelWeight (msg_server_validation.go) finds
// the validator and routes through the normal validation logic instead
// of returning ErrParticipantNotFound and aborting simsx.
//
// Approach: instead of calling EpochGroup.AddMember — which
// dispatches through the cosmos `group` module via UpdateGroupMembers
// and swallows sub-group AddMember errors at addToModelGroups:309-312 —
// we mutate `EpochGroupData.ValidationWeights` directly via
// SetEpochGroupData. The on-chain MsgValidation handler reads from the
// transient cache, which BuildEpochDataTransientCache populates from
// `EpochGroupData.ValidationWeights` (epoch_data_transient_cache.go).
// Skipping the group-module roundtrip removes both an
// unobservable failure path and the unnecessary write to GroupKeeper
// state.
//
// Sim-only bridge over production's PoC validator-rotation flow
// (activeparticipants.go addNewActiveParticipantsAsValidators) — no
// production code change.
//
// Idempotent per sub-group: appends only addresses not already in
// ValidationWeights.
//
// Weight=1, Reputation=0, VotingPower=1 chosen as the minimal non-zero
// values required by the validation arithmetic (zero weight would
// confuse vote-tallying division).
//
// After all sub-groups are updated, BuildEpochDataTransientCache is
// invoked so MsgValidation in the SAME block sees the freshly-populated
// cache (BeginBlocker rebuilds at block start; this mid-block rebuild
// avoids the one-block latency window).
//
// Tolerance: no current epoch group ⇒ nil (unit-test path).
func EnsureMembersInEpochGroup(ctx context.Context, k keeper.Keeper) error {
	eg, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		if errors.Is(err, types.ErrEffectiveEpochNotFound) || errors.Is(err, types.ErrEpochGroupDataNotFound) {
			return nil
		}
		return err
	}

	currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		return err
	}

	addrs := make([]string, 0, 8)
	iter, err := k.ActiveParticipantsSet.Iterate(ctx,
		collections.NewPrefixedPairRange[uint64, sdk.AccAddress](currentEpoch))
	if err != nil {
		return err
	}
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			iter.Close()
			return err
		}
		addrs = append(addrs, key.K2().String())
	}
	if closeErr := iter.Close(); closeErr != nil {
		return closeErr
	}
	if len(addrs) == 0 {
		return nil
	}

	// Update each sub-group's EpochGroupData ValidationWeights
	// (the actual source the transient cache builds from). Plus
	// the root group, so root-level meta lookups also work.
	groupKeys := append([]string{""}, eg.GroupData.SubGroupModels...)
	anyAdded := false
	for _, modelID := range groupKeys {
		data, found := k.GetEpochGroupData(ctx, currentEpoch, modelID)
		if !found {
			continue
		}
		have := make(map[string]bool, len(data.ValidationWeights))
		for _, vw := range data.ValidationWeights {
			if vw != nil {
				have[vw.MemberAddress] = true
			}
		}
		added := false
		for _, addr := range addrs {
			if have[addr] {
				continue
			}
			data.ValidationWeights = append(data.ValidationWeights, &types.ValidationWeight{
				MemberAddress: addr,
				Weight:        1,
				Reputation:    0,
				VotingPower:   1,
			})
			data.TotalWeight += 1
			added = true
		}
		if added {
			k.SetEpochGroupData(ctx, data)
			anyAdded = true
		}
	}

	// Rebuild the transient cache only when ValidationWeights actually
	// changed — MsgValidationFactory calls this per-op, and once the epoch's
	// members are seeded every later call would otherwise rebuild for nothing.
	if !anyAdded {
		return nil
	}
	return k.BuildEpochDataTransientCache(ctx)
}

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
// Determinism: GetGovernanceModels returns models in collections-sorted
// order (Models is a collections.Map keyed by string Id).
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

// EnsureActiveParticipantsSeeded promotes all registered Participants into
// ActiveParticipantsSet[currentEpoch] if it is currently empty. Sim-only
// bridge from the Participants seeded at genesis over the gap that the
// production PoC-epoch flow (activeparticipants.go) would normally
// close — issue #982 demands operations «honor message preconditions»,
// while faithfully simulating the full PoC epoch flow is a separate effort.
//
// Idempotent: any (currentEpoch, *) entry already present ⇒ no-op.
//
// Determinism: GetAllParticipant returns participants in bech32-sorted
// order (collections.Map invariant), so the ActiveParticipants slice is
// identical across same-seed runs.
//
// No msg-server flow, no production semantics change.
func EnsureActiveParticipantsSeeded(ctx context.Context, k keeper.Keeper) error {
	currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		return err
	}

	iter, err := k.ActiveParticipantsSet.Iterate(ctx,
		collections.NewPrefixedPairRange[uint64, sdk.AccAddress](currentEpoch))
	if err != nil {
		return err
	}
	seeded := iter.Valid()
	if closeErr := iter.Close(); closeErr != nil {
		return closeErr
	}
	if seeded {
		return nil
	}

	participants := k.GetAllParticipant(ctx)
	if len(participants) == 0 {
		return nil
	}

	active := make([]*types.ActiveParticipant, 0, len(participants))
	for _, p := range participants {
		active = append(active, &types.ActiveParticipant{
			Index: p.Index,
		})
	}
	return k.SetActiveParticipantsCache(ctx, types.ActiveParticipants{
		EpochId:      currentEpoch,
		Participants: active,
	})
}

// EnsureComputeValidators keeps the cometbft validator set alive across long
// simulation runs.
//
// Sim-only bridge over production's PoC validator-rotation flow. In production,
// cosmos x/slashing downtime-jails offline validators, but every epoch
// onSetNewValidatorsStage (module.go) calls SetComputeValidators, whose
// updateValidator (cosmos-sdk x/staking/keeper/compute.go:492) sets
// Jailed=false / Status=Bonded for every validator still doing PoC work — so the
// validator set is continuously refreshed and never drains.
//
// The simulation does not run PoC, so SetComputeValidators is never invoked and
// downtime-jailed validators are never un-jailed. Over a long run every
// validator jails out, the set empties, and simsx aborts with "empty validator
// set" (cosmos-sdk x/simulation/simulate.go:266) — observed draining bonded
// validators 131->83->35->0 over blocks 56->110->~135.
//
// This helper mimics the production refresh: when the bonded validator count
// falls below simBondedValidatorFloor it feeds every current validator back
// through SetComputeValidators, which un-jails and re-bonds them. Idempotent —
// above the floor it is a cheap no-op, and SetComputeValidators itself skips
// validators already bonded at the target power (compute.go:176).
//
// Determinism: GetAllValidators returns collections-sorted output and
// SetComputeValidators sorts its inputs internally (compute.go:140-163), so the
// refresh reproduces across same-seed runs. No production code change.
func EnsureComputeValidators(ctx context.Context, k keeper.Keeper) error {
	validators, err := k.Staking.GetAllValidators(ctx)
	if err != nil {
		return err
	}
	if len(validators) == 0 {
		return nil
	}

	bonded := 0
	for i := range validators {
		if validators[i].IsBonded() && !validators[i].IsJailed() {
			bonded++
		}
	}
	if bonded >= simBondedValidatorFloor {
		return nil
	}

	results := make([]stakingkeeper.ComputeResult, 0, len(validators))
	for i := range validators {
		pubKey, err := validators[i].ConsPubKey()
		if err != nil {
			// A validator with an undecodable consensus key cannot be fed back;
			// skip it rather than aborting the whole refresh.
			continue
		}
		results = append(results, stakingkeeper.ComputeResult{
			Power:           simValidatorPower,
			ValidatorPubKey: pubKey,
			OperatorAddress: validators[i].OperatorAddress,
		})
	}
	// isTestnet=true selects the current SetComputeValidators path (the legacy
	// pre-ValidatorIndexFixHeight branch only matters for mainnet's early
	// history); matches what mainnet runs today.
	_, err = k.Staking.SetComputeValidators(ctx, results, true)
	return err
}
