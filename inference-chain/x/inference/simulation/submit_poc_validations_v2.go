package simulation

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// MsgSubmitPocValidationsV2Factory creates a factory for MsgSubmitPocValidationsV2.
//
// Handler invariants the factory satisfies:
//   - window: IsValidationExchangeWindow (msg_server_poc_validations_v2.go)
//   - idempotency per (validator, participant, model, stage): factory
//     pre-checks HasPocValidationV2 so the skip reason surfaces in the
//     sim report rather than as an in-handler skip count
//
// Validation references a (participant, model) pair from earlier
// commits; factory skips if no commit exists for the chosen target so
// the validation surface stays consistent with what production
// validators would observe.
//
// Self-validation is not forbidden by the handler, so the factory does
// not exclude validator == target.
//
// Implements HasFutureOpsRegistry: every skip and every success books
// the next epoch's validation window onto the future-op queue.
//
// Requires Params.PocParams.PocV2Enabled=true.
func MsgSubmitPocValidationsV2Factory(k keeper.Keeper) simsx.SimMsgFactoryX {
	st := newPocFactoryState()
	var self simsx.SimMsgFactoryX
	self = simsx.NewSimMsgFactoryWithFutureOps[*types.MsgSubmitPocValidationsV2](
		func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter, fOpsReg simsx.FutureOpsRegistry) ([]simsx.SimAccount, *types.MsgSubmitPocValidationsV2) {
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
			windowStart := epochContext.StartOfPoCValidation() + 1
			if !epochContext.IsValidationExchangeWindow(currentBlockHeight) {
				w := epochContext.ValidationExchangeWindow()
				if currentBlockHeight < windowStart {
					st.scheduleForEpoch(ctx, fOpsReg, upcomingEpoch.Index, currentBlockHeight, windowStart, self)
				} else {
					nextWindowStart := epochContext.NextPoCStart() + params.EpochParams.GetStartOfPoCValidationStage() + 1
					st.scheduleForEpoch(ctx, fOpsReg, upcomingEpoch.Index+1, currentBlockHeight, nextWindowStart, self)
				}
				reporter.Skipf("outside validation exchange window (h=%d, [%d, %d])",
					currentBlockHeight, w.Start, w.End)
				return nil, nil
			}

			validator, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
			if !ok {
				return nil, nil
			}
			stage := epochContext.StartOfPoC()

			// Iterate PoCV2StoreCommits at this stage to find a (target,
			// modelID) the validator hasn't validated yet. Random target
			// selection over the N-participants × len(SimModelIDs) matrix
			// would almost always miss the sparse subset of slots that
			// have landed commits.
			type validationCandidate struct {
				target  sdk.AccAddress
				modelID string
				count   uint32
			}
			iter, err := k.PoCV2StoreCommits.Iterate(ctx,
				collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](stage))
			if err != nil {
				reporter.Skipf("PoCV2StoreCommits iterate failed: %v", err)
				return nil, nil
			}
			defer iter.Close()
			var pick validationCandidate
			candidateCount := 0
			for ; iter.Valid(); iter.Next() {
				key, kerr := iter.Key()
				if kerr != nil {
					reporter.Skipf("PoCV2StoreCommits key decode failed: %v", kerr)
					return nil, nil
				}
				targetAddr := key.K2()
				modelCandidate := key.K3()
				dup, _ := k.HasPocValidationV2(ctx, stage, targetAddr.String(), modelCandidate, validator.AddressBech32)
				if dup {
					continue
				}
				val, verr := iter.Value()
				if verr != nil {
					reporter.Skipf("PoCV2StoreCommits value decode failed: %v", verr)
					return nil, nil
				}
				candidate := validationCandidate{target: targetAddr, modelID: modelCandidate, count: val.Count}
				// Reservoir sampling: each candidate replaces the current
				// pick with probability 1/(candidateCount+1), yielding a
				// uniform pick across all encountered candidates without
				// materializing the full slice.
				if candidateCount == 0 || testData.Rand().Rand.Intn(candidateCount+1) == 0 {
					pick = candidate
				}
				candidateCount++
			}
			if candidateCount == 0 {
				reporter.Skipf("validator %s has nothing left to validate at stage=%d",
					validator.AddressBech32, stage)
				return nil, nil
			}
			targetBech32 := pick.target.String()
			modelID := pick.modelID

			msg := &types.MsgSubmitPocValidationsV2{
				Creator:                  validator.AddressBech32,
				PocStageStartBlockHeight: stage,
				Validations: []*types.PoCValidationEntryV2{
					{
						ParticipantAddress: targetBech32,
						ModelId:            modelID,
						ValidatedWeight:    int64(pick.count),
					},
				},
			}
			nextWindowStart := epochContext.NextPoCStart() + params.EpochParams.GetStartOfPoCValidationStage() + 1
			st.scheduleForEpoch(ctx, fOpsReg, upcomingEpoch.Index+1, currentBlockHeight, nextWindowStart, self)
			return []simsx.SimAccount{validator}, msg
		},
	)
	return self
}
