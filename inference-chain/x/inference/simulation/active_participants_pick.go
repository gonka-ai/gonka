package simulation

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// collectInferencesByStatus returns every Inference in the keeper whose
// Status matches. Iteration order is collections-sorted by InferenceId,
// so a same-seed pick over the result is deterministic.
func collectInferencesByStatus(
	ctx context.Context,
	k keeper.Keeper,
	status types.InferenceStatus,
) ([]types.Inference, error) {
	iter, err := k.Inferences.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := make([]types.Inference, 0, 16)
	for ; iter.Valid(); iter.Next() {
		val, err := iter.Value()
		if err != nil {
			return nil, err
		}
		if val.Status == status {
			out = append(out, val)
		}
	}
	return out, nil
}

// PickRandomFinishedInference returns a FINISHED-status Inference from the
// on-chain Inferences collection. If none exists yet (no Start+Finish pair
// has completed) the reporter is Skipped and (zero, false) is returned.
//
// Used by MsgValidationFactory: passing a STARTED-status inference to
// MsgValidation triggers the hard ErrInferenceNotFinished path
// (msg_server_validation.go) which would abort the simsx run.
// Skip-on-empty keeps the simulation progressing until at least one
// Start→Finish pair lands.
//
// Returns the full Inference, not just its ID — collectInferencesByStatus
// already deserialized it, so the caller needs no second keeper read.
func PickRandomFinishedInference(
	ctx context.Context,
	k keeper.Keeper,
	testData *simsx.ChainDataSource,
	reporter simsx.SimulationReporter,
) (types.Inference, bool) {
	finished, err := collectInferencesByStatus(ctx, k, types.InferenceStatus_FINISHED)
	if err != nil {
		reporter.Skipf("Inferences scan failed: %v", err)
		return types.Inference{}, false
	}
	if len(finished) == 0 {
		reporter.Skip("no FINISHED inferences in keeper yet")
		return types.Inference{}, false
	}
	return finished[testData.Rand().Intn(len(finished))], true
}

// FindRandomStartedInference returns a random STARTED-status inference
// whose AssignedTo address is a known sim account (so the Finish factory
// can sign as it). Returns (zero, false) when none qualifies — the
// caller then falls back to a fresh finish-first message rather than
// Skipping. Does NOT touch the reporter.
func FindRandomStartedInference(
	ctx context.Context,
	k keeper.Keeper,
	testData *simsx.ChainDataSource,
) (types.Inference, bool) {
	started, err := collectInferencesByStatus(ctx, k, types.InferenceStatus_STARTED)
	if err != nil || len(started) == 0 {
		return types.Inference{}, false
	}
	eligible := started[:0]
	for _, inf := range started {
		if inf.AssignedTo != "" && testData.HasAccount(inf.AssignedTo) {
			eligible = append(eligible, inf)
		}
	}
	if len(eligible) == 0 {
		return types.Inference{}, false
	}
	return eligible[testData.Rand().Intn(len(eligible))], true
}

// PickRandomGovernanceModelID returns a model Id from the on-chain
// Models collection at random. If empty (no models registered) the
// reporter is Skipped and ("", false) is returned. Deterministic across
// same-seed runs because GetGovernanceModels returns the
// collections-sorted (bech32-key string) view.
func PickRandomGovernanceModelID(
	ctx context.Context,
	k keeper.Keeper,
	testData *simsx.ChainDataSource,
	reporter simsx.SimulationReporter,
) (string, bool) {
	models, err := k.GetGovernanceModels(ctx)
	if err != nil {
		reporter.Skipf("GetGovernanceModels failed: %v", err)
		return "", false
	}
	if len(models) == 0 {
		reporter.Skip("no governance models registered")
		return "", false
	}
	return models[testData.Rand().Intn(len(models))].Id, true
}

// PickRandomActiveSimAccountExcluding behaves like PickRandomActiveSimAccount
// but draws from a CALLER-SPECIFIED epoch's ActiveParticipantsSet and filters
// out the given address. MsgValidationFactory must pick a validator from the
// *inference's* epoch — not the current epoch — because the handler keys the
// transient validation cache by inference.EpochId (GetCachedEpochDataModelWeight,
// msg_server_validation.go). Drawing from the current epoch breaks after an
// epoch transition: a current-epoch validator is absent from a previous-epoch
// inference's cache and the handler hard-fails with ErrParticipantNotFound.
// Also satisfies validator!=executor (msg_server_validation.go).
func PickRandomActiveSimAccountExcluding(
	ctx context.Context,
	k keeper.Keeper,
	testData *simsx.ChainDataSource,
	reporter simsx.SimulationReporter,
	epoch uint64,
	excludeAddr string,
) (simsx.SimAccount, bool) {
	iter, err := k.ActiveParticipantsSet.Iterate(ctx,
		collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epoch))
	if err != nil {
		reporter.Skipf("ActiveParticipantsSet iterate failed: %v", err)
		return simsx.SimAccount{}, false
	}
	defer iter.Close()

	addrs := make([]sdk.AccAddress, 0, 8)
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			reporter.Skipf("ActiveParticipantsSet key decode failed: %v", err)
			return simsx.SimAccount{}, false
		}
		bech := key.K2().String()
		if bech == excludeAddr {
			continue
		}
		addrs = append(addrs, key.K2())
	}
	if len(addrs) == 0 {
		reporter.Skipf("no active participants in epoch %d other than %s", epoch, excludeAddr)
		return simsx.SimAccount{}, false
	}
	pick := addrs[testData.Rand().Intn(len(addrs))]
	if !testData.HasAccount(pick.String()) {
		reporter.Skipf("active participant %s not in sim accounts", pick.String())
		return simsx.SimAccount{}, false
	}
	return testData.GetAccount(reporter, pick.String()), true
}

// PickRandomActiveSimAccount returns a sim account whose address is in
// ActiveParticipantsSet[currentEpoch]. On any miss (empty set, address not
// in sim accounts, keeper error) the reporter is Skipped with an
// informative message and (zero, false) is returned.
//
// Determinism: ActiveParticipantsSet iteration is collections-sorted; the
// pick index is drawn from testData.Rand() so the same seed reproduces the
// same choice.
//
// Pairs with EnsureActiveParticipantsSeeded (bootstrap.go): factories
// should call EnsureActiveParticipantsSeeded first so this picker has a
// non-empty set to draw from.
func PickRandomActiveSimAccount(
	ctx context.Context,
	k keeper.Keeper,
	testData *simsx.ChainDataSource,
	reporter simsx.SimulationReporter,
) (simsx.SimAccount, bool) {
	currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		reporter.Skipf("EffectiveEpochIndex get failed: %v", err)
		return simsx.SimAccount{}, false
	}
	iter, err := k.ActiveParticipantsSet.Iterate(ctx,
		collections.NewPrefixedPairRange[uint64, sdk.AccAddress](currentEpoch))
	if err != nil {
		reporter.Skipf("ActiveParticipantsSet iterate failed: %v", err)
		return simsx.SimAccount{}, false
	}
	defer iter.Close()

	addrs := make([]sdk.AccAddress, 0, 8)
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			reporter.Skipf("ActiveParticipantsSet key decode failed: %v", err)
			return simsx.SimAccount{}, false
		}
		addrs = append(addrs, key.K2())
	}
	if len(addrs) == 0 {
		reporter.Skip("no active participants in current epoch")
		return simsx.SimAccount{}, false
	}

	pick := addrs[testData.Rand().Intn(len(addrs))]
	if !testData.HasAccount(pick.String()) {
		reporter.Skipf("active participant %s not in sim accounts", pick.String())
		return simsx.SimAccount{}, false
	}
	return testData.GetAccount(reporter, pick.String()), true
}
