package simulation

import (
	"context"
	"encoding/hex"
	"math/rand"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// SimulateMsgSubmitSeed is a legacy simtypes.Operation stub kept for the
// non-simsx WeightedOperations() / ProposalMsgs() hooks. simsx prefers
// WeightedOperationsX (module/simulation.go), which routes to
// MsgSubmitSeedFactory below. This stub is reachable only by the legacy
// runner.
func SimulateMsgSubmitSeed(
	ak types.AccountKeeper,
	bk types.BankKeeper,
	k keeper.Keeper,
) simtypes.Operation {
	return func(r *rand.Rand, app *baseapp.BaseApp, ctx sdk.Context, accs []simtypes.Account, chainID string,
	) (simtypes.OperationMsg, []simtypes.FutureOperation, error) {
		simAccount, _ := simtypes.RandomAcc(r, accs)
		msg := &types.MsgSubmitSeed{
			Creator: simAccount.Address.String(),
		}
		return simtypes.NoOpMsg(types.ModuleName, sdk.MsgTypeURL(msg), "SubmitSeed legacy stub — simsx uses MsgSubmitSeedFactory"), nil, nil
	}
}

// MsgSubmitSeedFactory creates a factory for MsgSubmitSeed.
//
// Handler invariants the factory satisfies:
//   - permission: ParticipantPermission (msg_server_submit_seed.go)
//   - msg.EpochIndex in [currentEpoch, currentEpoch+1]
//   - ValidateBasic: Creator bech32, EpochIndex > 0, Signature is hex-128
//     (utils.ValidateHexRSig64) — shape only, no crypto verification
//   - idempotency: RandomSeeds.Set is unconditional, so the factory
//     pre-checks GetRandomSeed to surface the duplicate as a sim skip
//     rather than a redundant state write
//
// Targets upcomingEpoch.Index so ComputeNewWeights' seed lookup
// (chainvalidation.go:949 — `GetRandomSeed(ctx, upcomingEpoch.Index, ...)`)
// finds a seed for every committing participant. Without seeds, every
// PoCV2StoreCommit is filtered out, `pocMiningParticipants` stays empty
// and `onEndOfPoCValidationStage` returns `computeResult == nil` —
// halting the chain at the next epoch transition.
//
// Implements HasFutureOpsRegistry: every skip and every success books
// the next PoC exchange window onto the future-op queue.
func MsgSubmitSeedFactory(k keeper.Keeper) simsx.SimMsgFactoryX {
	st := newPocFactoryState()
	var self simsx.SimMsgFactoryX
	self = simsx.NewSimMsgFactoryWithFutureOps[*types.MsgSubmitSeed](
		func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter, fOpsReg simsx.FutureOpsRegistry) ([]simsx.SimAccount, *types.MsgSubmitSeed) {
			sdkCtx := sdk.UnwrapSDKContext(ctx)
			currentBlockHeight := sdkCtx.BlockHeight()

			upcomingEpoch, found := k.GetUpcomingEpoch(ctx)
			if !found || upcomingEpoch == nil {
				reporter.Skipf("no upcoming epoch")
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

			ta, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
			if !ok {
				return nil, nil
			}

			if _, exists := k.GetRandomSeed(ctx, upcomingEpoch.Index, ta.AddressBech32); exists {
				reporter.Skipf("seed already submitted for (%s, epoch=%d)", ta.AddressBech32, upcomingEpoch.Index)
				return nil, nil
			}

			// Handler does not verify the signature cryptographically — only
			// shape (utils.ValidateHexRSig64: hex-128 = 64 bytes). Random
			// payload is sufficient and keeps the sim deterministic via
			// testData.Rand().
			sigBytes := make([]byte, 64)
			testData.Rand().Read(sigBytes)

			msg := &types.MsgSubmitSeed{
				Creator:    ta.AddressBech32,
				EpochIndex: upcomingEpoch.Index,
				Signature:  hex.EncodeToString(sigBytes),
			}
			st.scheduleForEpoch(ctx, fOpsReg, upcomingEpoch.Index+1, currentBlockHeight, epochContext.NextPoCStart()+1, self)
			return []simsx.SimAccount{ta}, msg
		},
	)
	return self
}
