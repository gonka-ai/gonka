package simulation

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// MsgMLNodeWeightDistributionFactory creates a factory for MsgMLNodeWeightDistribution.
//
// Handler invariants the factory satisfies:
//   - window: PoC start through EndOfPoCValidation
//     (msg_server_poc_v2_commit.go)
//   - existing PoCV2StoreCommit required for (participant, model, stage):
//     factory pre-checks PoCV2StoreCommits.Has, skips if absent
//   - sum(Entry.Weights[*].Weight) == commit.Count: factory reads the
//     commit and emits a single MLNodeWeight at full weight
//   - idempotency: MLNodeWeightDistributions.Has pre-check
//
// Implements HasFutureOpsRegistry: every skip and every success books
// the next target window. Target height is StartOfPoC+2, two blocks
// after MsgPoCV2StoreCommitFactory's StartOfPoC+1 target, so the
// store commit is durable in KV before this factory's idempotency
// check reads it.
//
// Requires Params.PocParams.PocV2Enabled=true.
func MsgMLNodeWeightDistributionFactory(k keeper.Keeper) simsx.SimMsgFactoryX {
	st := newPocFactoryState()
	var self simsx.SimMsgFactoryX
	self = simsx.NewSimMsgFactoryWithFutureOps[*types.MsgMLNodeWeightDistribution](
		func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter, fOpsReg simsx.FutureOpsRegistry) ([]simsx.SimAccount, *types.MsgMLNodeWeightDistribution) {
			sdkCtx := sdk.UnwrapSDKContext(ctx)
			currentBlockHeight := sdkCtx.BlockHeight()

			upcomingEpoch, found := k.GetUpcomingEpoch(ctx)
			if !found || upcomingEpoch == nil {
				reporter.Skipf("no upcoming epoch (pre-PoC genesis or post-rotation gap)")
				return nil, nil
			}
			params, err := k.GetParams(ctx)
			if err != nil {
				reporter.Skipf("GetParams failed: %v", err)
				return nil, nil
			}
			epochContext := types.NewEpochContext(*upcomingEpoch, *params.EpochParams)
			stage := epochContext.StartOfPoC()
			windowStart := stage + 2
			if currentBlockHeight < stage || currentBlockHeight > epochContext.EndOfPoCValidation() {
				if currentBlockHeight < stage {
					st.scheduleForEpoch(ctx, fOpsReg, upcomingEpoch.Index, currentBlockHeight, windowStart, self)
				} else {
					st.scheduleForEpoch(ctx, fOpsReg, upcomingEpoch.Index+1, currentBlockHeight, epochContext.NextPoCStart()+2, self)
				}
				reporter.Skipf("outside PoC+validation window (h=%d, [%d, %d])",
					currentBlockHeight, stage, epochContext.EndOfPoCValidation())
				return nil, nil
			}

			ta, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
			if !ok {
				return nil, nil
			}

			// Read PoCV2StoreCommits to find a (model) slot for this
			// participant that has a commit but no distribution yet. Random
			// model selection over the SimModelIDs × N-participants matrix
			// would almost always miss the sparse subset of slots that have
			// landed commits.
			var modelID string
			var key collections.Triple[int64, sdk.AccAddress, string]
			picked := false
			order := testData.Rand().Rand.Perm(len(SimModelIDs))
			for _, idx := range order {
				candidate := SimModelIDs[idx]
				k3 := collections.Join3(stage, ta.Address, candidate)
				hasCommit, _ := k.PoCV2StoreCommits.Has(ctx, k3)
				if !hasCommit {
					continue
				}
				if hasDist, _ := k.MLNodeWeightDistributions.Has(ctx, k3); hasDist {
					continue
				}
				modelID, key, picked = candidate, k3, true
				break
			}
			if !picked {
				reporter.Skipf("no pending (commit AND not-yet-distributed) slot for participant %s at stage=%d",
					ta.AddressBech32, stage)
				return nil, nil
			}

			// Read commit to learn Count (handler validates sum(weights)==commit.Count).
			commit, err := k.PoCV2StoreCommits.Get(ctx, key)
			if err != nil {
				reporter.Skipf("PoCV2StoreCommits.Get failed: %v", err)
				return nil, nil
			}

			// Build single-node distribution with full weight == commit.Count.
			// NodeId pattern matches activeParticipantFromSimParticipant
			// (bootstrap.go:79) so downstream lookups by NodeId align.
			msg := &types.MsgMLNodeWeightDistribution{
				Creator:                  ta.AddressBech32,
				PocStageStartBlockHeight: stage,
				Entries: []*types.MLNodeDistributionEntry{
					{
						ModelId: modelID,
						Weights: []*types.MLNodeWeight{
							{
								NodeId: ta.AddressBech32 + "/" + modelID,
								Weight: commit.Count,
							},
						},
					},
				},
			}
			st.scheduleForEpoch(ctx, fOpsReg, upcomingEpoch.Index+1, currentBlockHeight, epochContext.NextPoCStart()+2, self)
			return []simsx.SimAccount{ta}, msg
		},
	)
	return self
}
