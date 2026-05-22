package simulation

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/cosmos/cosmos-sdk/x/group"

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

// PickRandomFinishedInference returns a FINISHED-status Inference whose
// EpochId is within the last two epochs (inference.EpochId >=
// currentEpoch-1). Older inferences are filtered out because the
// MsgValidation handler keys its transient validation cache by EpochId;
// stale epochs no longer have a live ActiveParticipantsSet entry, so
// PickRandomActiveSimAccountExcluding would return a validator whose
// permission check fails the handler with «participant is not active»
// and aborts the simsx run. If none qualifies the reporter is Skipped
// and (zero, false) is returned.
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
	currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		reporter.Skipf("EffectiveEpochIndex get failed: %v", err)
		return types.Inference{}, false
	}
	finished, err := collectInferencesByStatus(ctx, k, types.InferenceStatus_FINISHED)
	if err != nil {
		reporter.Skipf("Inferences scan failed: %v", err)
		return types.Inference{}, false
	}
	if len(finished) == 0 {
		reporter.Skip("no FINISHED inferences in keeper yet")
		return types.Inference{}, false
	}
	// inf.EpochId+1 >= currentEpoch guards against uint64 underflow at
	// currentEpoch == 0; at currentEpoch == N the test admits EpochIds
	// {N-1, N, N+1, ...}, i.e. anything not strictly older than N-1.
	fresh := finished[:0]
	for _, inf := range finished {
		if currentEpoch == 0 || inf.EpochId+1 >= currentEpoch {
			fresh = append(fresh, inf)
		}
	}
	if len(fresh) == 0 {
		reporter.Skipf("no FINISHED inferences in last two epochs (current=%d)", currentEpoch)
		return types.Inference{}, false
	}
	return fresh[testData.Rand().Intn(len(fresh))], true
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

// PickRandomSupportedGovernanceModelID returns a random governance model that
// the given active participant supports in the specified epoch. Start/finish
// factories use this after choosing an executor so generated inferences do not
// assign that executor to a model sub-group it does not belong to.
func PickRandomSupportedGovernanceModelID(
	ctx context.Context,
	k keeper.Keeper,
	testData *simsx.ChainDataSource,
	reporter simsx.SimulationReporter,
	epoch uint64,
	participantAddr string,
) (string, bool) {
	active, found := k.GetActiveParticipants(ctx, epoch)
	if !found {
		reporter.Skipf("ActiveParticipants for epoch %d not found", epoch)
		return "", false
	}

	var supported []string
	for _, participant := range active.Participants {
		if participant == nil || participant.Index != participantAddr {
			continue
		}
		for _, modelID := range participant.Models {
			if _, found := k.GetGovernanceModel(ctx, modelID); found {
				supported = append(supported, modelID)
			}
		}
		break
	}
	if len(supported) == 0 {
		reporter.Skipf("active participant %s has no supported governance models in epoch %d", participantAddr, epoch)
		return "", false
	}
	return supported[testData.Rand().Intn(len(supported))], true
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
	currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		reporter.Skipf("EffectiveEpochIndex get failed: %v", err)
		return simsx.SimAccount{}, false
	}
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
		excluded, err := k.ExcludedParticipantsMap.Has(ctx, collections.Join(currentEpoch, key.K2()))
		if err != nil {
			reporter.Skipf("ExcludedParticipantsMap lookup failed: %v", err)
			return simsx.SimAccount{}, false
		}
		if excluded {
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
// Pairs with BuildEpochSubstrate (substrate.go): factories should call
// BuildEpochSubstrate first so ActiveParticipants[currentEpoch] is
// populated and this picker has a non-empty set to draw from.
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
		excluded, err := k.ExcludedParticipantsMap.Has(ctx, collections.Join(currentEpoch, key.K2()))
		if err != nil {
			reporter.Skipf("ExcludedParticipantsMap lookup failed: %v", err)
			return simsx.SimAccount{}, false
		}
		if excluded {
			continue
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

// PickRandomVotingInferenceWithOpenProposal returns a VOTING-status inference
// whose x/group invalidation proposals are still inside their voting window
// (created in the current block).
//
// The validation group's voting period is 4 minutes (epochgroup/epoch_group.go
// CreateGroup), while the cosmos simulation advances block time by 5000-10000 s
// per block (x/simulation/simulate.go) — so a proposal is votable only in the
// very block it was submitted. Among the live inferences the one with the
// highest ReValidatePolicyId is returned: every revalidation op in a block then
// converges on the same freshest proposal pair, the only way to concentrate
// enough YES weight to cross the 50% PercentageDecisionPolicy quorum before the
// block ends. Deterministic (no rng) so same-seed runs reproduce.
//
// Returns (zero, false) when no VOTING inference has an open proposal.
func PickRandomVotingInferenceWithOpenProposal(
	ctx context.Context,
	k keeper.Keeper,
	groupKeeper types.GroupMessageKeeper,
) (types.Inference, bool) {
	voting, err := collectInferencesByStatus(ctx, k, types.InferenceStatus_VOTING)
	if err != nil || len(voting) == 0 {
		return types.Inference{}, false
	}
	var best types.Inference
	found := false
	for _, inf := range voting {
		if inf.ProposalDetails == nil || !proposalWindowOpen(ctx, groupKeeper, inf.ProposalDetails) {
			continue
		}
		if !found || inf.ProposalDetails.ReValidatePolicyId > best.ProposalDetails.ReValidatePolicyId {
			best, found = inf, true
		}
	}
	return best, found
}

// proposalWindowOpen reports whether the ReValidate proposal behind a VOTING
// inference is still SUBMITTED and before its VotingPeriodEnd. Both proposals
// from submitValidationProposalsWithPolicy (msg_server_validation.go) share one
// VotingPeriodEnd, so the ReValidate one stands in for the pair. The query is
// reverse-paginated: only the most recent proposals — those created this
// block — can still be open.
func proposalWindowOpen(ctx context.Context, groupKeeper types.GroupMessageKeeper, pd *types.ProposalDetails) bool {
	resp, err := groupKeeper.ProposalsByGroupPolicy(ctx, &group.QueryProposalsByGroupPolicyRequest{
		Address:    pd.PolicyAddress,
		Pagination: &query.PageRequest{Reverse: true, Limit: 128},
	})
	if err != nil || resp == nil {
		return false
	}
	blockTime := sdk.UnwrapSDKContext(ctx).BlockTime()
	for _, p := range resp.Proposals {
		if p.Id == pd.ReValidatePolicyId {
			return p.Status == group.PROPOSAL_STATUS_SUBMITTED && p.VotingPeriodEnd.After(blockTime)
		}
	}
	return false
}
