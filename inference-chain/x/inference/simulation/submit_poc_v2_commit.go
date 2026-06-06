package simulation

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// MsgPoCV2StoreCommitFactory creates a factory for MsgPoCV2StoreCommit.
//
// Handler invariants the factory satisfies:
//   - window: IsPoCExchangeWindow (msg_server_poc_v2_commit.go)
//   - monotonic Count per (participant, model, stage): factory submits at
//     most one commit per triple via PoCV2StoreCommits.Has pre-check
//   - entry shape: ModelId must be governance-registered, Count > 0,
//     RootHash exactly 32 bytes
//
// Implements HasFutureOpsRegistry: every skip and every success books
// the next target PoC window onto the future-op queue, so the factory
// keeps firing in its target window independently of how the random
// operation pump schedules it.
//
// Requires Params.PocParams.PocV2Enabled=true.
func MsgPoCV2StoreCommitFactory(k keeper.Keeper) simsx.SimMsgFactoryX {
	st := newPocFactoryState()
	var self simsx.SimMsgFactoryX
	self = simsx.NewSimMsgFactoryWithFutureOps[*types.MsgPoCV2StoreCommit](
		func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter, fOpsReg simsx.FutureOpsRegistry) ([]simsx.SimAccount, *types.MsgPoCV2StoreCommit) {
			sdkCtx := sdk.UnwrapSDKContext(ctx)
			currentBlockHeight := sdkCtx.BlockHeight()

			// Read upcoming epoch + epoch params to compute the PoC window.
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
			windowStart := epochContext.StartOfPoC() + 1
			if !epochContext.IsPoCExchangeWindow(currentBlockHeight) {
				if currentBlockHeight < windowStart {
					st.scheduleForEpoch(ctx, fOpsReg, upcomingEpoch.Index, currentBlockHeight, windowStart, self)
				} else {
					st.scheduleForEpoch(ctx, fOpsReg, upcomingEpoch.Index+1, currentBlockHeight, epochContext.NextPoCStart()+1, self)
				}
				reporter.Skipf("outside PoC exchange window (h=%d)", currentBlockHeight)
				return nil, nil
			}

			// Pick a sim genesis participant. Limits commits to the 5 sim genesis
			// accounts so ComputeNewWeights' inferred ActiveParticipant set
			// aligns with our intended validator set rather than throwaway
			// random simState.Accounts.
			ta, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
			if !ok {
				return nil, nil
			}

			// Pick a model (Kimi or Qwen — both in SimModelIDs / sim genesis).
			modelID := SimModelIDs[testData.Rand().Rand.Intn(len(SimModelIDs))]

			// Idempotency check: skip if commit already exists for this
			// (participant, model, stage). Avoids the monotonic-Count constraint.
			stage := epochContext.StartOfPoC()
			commitKey := collections.Join3(stage, ta.Address, modelID)
			has, _ := k.PoCV2StoreCommits.Has(ctx, commitKey)
			if has {
				reporter.Skipf("commit already exists for (%s, %s, stage=%d)",
					ta.AddressBech32, modelID, stage)
				return nil, nil
			}

			// Build the entry: random 32-byte RootHash + Count in [100, 999].
			rootHash := make([]byte, 32)
			testData.Rand().Read(rootHash)
			count := uint32(100 + testData.Rand().Rand.Intn(900))

			msg := &types.MsgPoCV2StoreCommit{
				Creator:                  ta.AddressBech32,
				PocStageStartBlockHeight: stage,
				Entries: []*types.PoCV2CommitEntry{
					{
						ModelId:  modelID,
						Count:    count,
						RootHash: rootHash,
					},
				},
			}
			st.scheduleForEpoch(ctx, fOpsReg, upcomingEpoch.Index+1, currentBlockHeight, epochContext.NextPoCStart()+1, self)
			return []simsx.SimAccount{ta}, msg
		},
	)
	return self
}
