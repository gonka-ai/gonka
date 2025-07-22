package keeper

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) ClaimRewards(goCtx context.Context, msg *types.MsgClaimRewards) (*types.MsgClaimRewardsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	settleAmount, response := k.validateRequest(ctx, msg)
	if response != nil {
		return response, nil
	}

	response, err := k.validateClaim(ctx, msg, settleAmount)
	if err != nil {
		k.LogError("Claim validation failed", types.Claims, "error", err, "account", msg.Creator)
		return response, nil
	}
	k.LogDebug("Claim verified", types.Claims, "account", msg.Creator, "seed", msg.Seed)

	response, err = k.payoutClaim(ctx, msg, settleAmount)
	if err != nil {
		k.LogError("Claim payout failed", types.Claims, "error", err, "account", msg.Creator)
		return response, nil
	}

	return response, nil
}

func (ms msgServer) payoutClaim(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount *types.SettleAmount) (*types.MsgClaimRewardsResponse, error) {
	ms.LogInfo("Issuing rewards", types.Claims, "address", msg.Creator, "amount", settleAmount.GetTotalCoins())

	// Pay for work from escrow
	escrowPayment := settleAmount.GetWorkCoins()
	if err := ms.PayParticipantFromEscrow(ctx, msg.Creator, escrowPayment, "work_coins:"+settleAmount.Participant); err != nil {
		if sdkerrors.ErrInsufficientFunds.Is(err) {
			ms.handleUnderfundedWork(ctx, err, settleAmount)
			return &types.MsgClaimRewardsResponse{
				Amount: 0,
				Result: "Insufficient funds for paying participant for work! Unpaid settlement",
			}, err
		}
		ms.LogError("Error paying participant from escrow", types.Claims, "error", err)
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "Error paying participant from escrow",
		}, err
	}
	ms.AddTokenomicsData(ctx, &types.TokenomicsData{TotalFees: settleAmount.GetWorkCoins()})

	// Pay rewards from module
	if err := ms.PayParticipantFromModule(ctx, msg.Creator, settleAmount.GetRewardCoins(), types.ModuleName, "reward_coins:"+settleAmount.Participant); err != nil {
		if sdkerrors.ErrInsufficientFunds.Is(err) {
			ms.LogError("Insufficient funds for paying rewards. Work paid, rewards declined", types.Claims, "error", err, "settleAmount", settleAmount)
		} else {
			ms.LogError("Error paying participant for rewards", types.Claims, "error", err)
		}
		ms.finishSettle(ctx, settleAmount)
		return &types.MsgClaimRewardsResponse{
			Amount: settleAmount.GetWorkCoins(),
			Result: "Work paid, but rewards failed.",
		}, err
	}

	ms.finishSettle(ctx, settleAmount)
	return &types.MsgClaimRewardsResponse{
		Amount: settleAmount.GetTotalCoins(),
		Result: "Rewards claimed successfully",
	}, nil
}

func (ms msgServer) handleUnderfundedWork(ctx sdk.Context, err error, settleAmount *types.SettleAmount) {
	ms.LogError("Insufficient funds for paying participant for work! Unpaid settlement", types.Claims, "error", err, "settleAmount", settleAmount)

	spendable, required := ms.parseBalanceError(err.Error())
	ms.LogError("Balance details", types.Claims, "spendable", spendable, "required", required)

	ms.finishSettle(ctx, settleAmount)
}

func (ms msgServer) parseBalanceError(errMsg string) (spendable int64, required int64) {
	_, err := fmt.Sscanf(errMsg, "spendable balance %dnicoin is smaller than %dnicoin", &spendable, &required)
	if err != nil {
		return 0, 0
	}
	return spendable, required
}

func (ms msgServer) finishSettle(ctx sdk.Context, settleAmount *types.SettleAmount) {
	ms.RemoveSettleAmount(ctx, settleAmount.Participant)
	perfSummary, found := ms.GetEpochPerformanceSummary(ctx, settleAmount.PocStartHeight, settleAmount.Participant)
	if found {
		perfSummary.Claimed = true
		ms.SetEpochPerformanceSummary(ctx, perfSummary)
	}
}

func (k msgServer) validateRequest(ctx sdk.Context, msg *types.MsgClaimRewards) (*types.SettleAmount, *types.MsgClaimRewardsResponse) {
	settleAmount, found := k.GetSettleAmount(ctx, msg.Creator)
	if !found {
		k.LogDebug("SettleAmount not found for address", types.Claims, "address", msg.Creator)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this address",
		}
	}
	if settleAmount.PocStartHeight != msg.PocStartHeight {
		k.LogDebug("SettleAmount does not match height", types.Claims, "height", msg.PocStartHeight, "settleHeight", settleAmount.PocStartHeight)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this block height",
		}
	}
	if settleAmount.GetTotalCoins() == 0 {
		k.LogDebug("SettleAmount had zero coins", types.Claims, "address", msg.Creator)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this address",
		}
	}

	return &settleAmount, nil
}

func (k msgServer) validateClaim(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount *types.SettleAmount) (*types.MsgClaimRewardsResponse, error) {
	k.LogInfo("Validating claim", types.Claims, "account", msg.Creator, "seed", msg.Seed, "height", msg.PocStartHeight)

	// Validate the seed signature
	if err := k.validateSeedSignature(ctx, msg, settleAmount); err != nil {
		k.LogError("Seed signature validation failed", types.Claims, "error", err)
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "Seed signature validation failed",
		}, err
	}

	// Check for missed validations
	if validationMissed, err := k.hasMissedValidations(ctx, msg); err != nil {
		k.LogError("Failed to check for missed validations", types.Claims, "error", err)
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "Failed to check for missed validations",
		}, err
	} else if validationMissed {
		k.LogError("Inference not validated", types.Claims, "account", msg.Creator)
		// TODO: Report that validator has missed validations
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "Inference not validated",
		}, types.ErrValidationsMissed
	}

	return nil, nil
}

func (k msgServer) hasMissedValidations(ctx sdk.Context, msg *types.MsgClaimRewards) (bool, error) {
	mustBeValidated, err := k.getMustBeValidatedInferences(ctx, msg)
	if err != nil {
		return false, err
	}
	wasValidated := k.getValidatedInferences(ctx, msg)

	for _, inferenceId := range mustBeValidated {
		if !wasValidated[inferenceId] {
			return true, nil
		}
	}

	return false, nil
}

func (ms msgServer) validateSeedSignature(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount *types.SettleAmount) error {
	ms.LogDebug("Validating seed signature", types.Claims, "account", msg.Creator, "seed", msg.Seed, "height", msg.PocStartHeight)
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return types.ErrPocAddressInvalid
	}
	acc := ms.AccountKeeper.GetAccount(ctx, addr)
	if acc == nil {
		ms.LogError("Account not found for signature", types.Claims, "address", msg.Creator)
		return types.ErrParticipantNotFound
	}
	pubKey := acc.GetPubKey()
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(msg.Seed))
	signature, err := hex.DecodeString(settleAmount.SeedSignature)
	if err != nil {
		ms.LogError("Error decoding signature", types.Claims, "error", err)
		return err
	}
	ms.LogDebug("Verifying signature", types.Claims, "seedBytes", hex.EncodeToString(seedBytes), "signature", hex.EncodeToString(signature), "pubkey", pubKey.String())
	if !pubKey.VerifySignature(seedBytes, signature) {
		ms.LogError("Signature verification failed", types.Claims, "seed", msg.Seed, "signature", settleAmount.SeedSignature, "seedBytes", hex.EncodeToString(seedBytes))
		return types.ErrClaimSignatureInvalid
	}
	return nil
}

func (k msgServer) getValidatedInferences(ctx sdk.Context, msg *types.MsgClaimRewards) map[string]bool {
	wasValidatedRaw, found := k.GetEpochGroupValidations(ctx, msg.Creator, msg.PocStartHeight)
	if !found {
		k.LogInfo("Validations not found", types.Claims, "height", msg.PocStartHeight, "account", msg.Creator)
		wasValidatedRaw = types.EpochGroupValidations{
			ValidatedInferences: make([]string, 0),
		}
	}

	wasValidated := make(map[string]bool)
	for _, inferenceId := range wasValidatedRaw.ValidatedInferences {
		wasValidated[inferenceId] = true
	}
	return wasValidated
}

func (k msgServer) getEpochGroupWeightData(ctx sdk.Context, pocStartHeight uint64, modelId string) (*types.EpochGroupData, map[string]types.ValidationWeight, int64, bool) {
	epochData, found := k.GetEpochGroupData(ctx, pocStartHeight, modelId)
	if !found {
		if modelId == "" {
			k.LogError("Epoch data not found", types.Claims, "height", pocStartHeight)
		} else {
			k.LogWarn("Sub epoch data not found", types.Claims, "height", pocStartHeight, "modelId", modelId)
		}
		return nil, nil, 0, false
	}

	// Build weight map and total weight for the epoch group
	weightMap := make(map[string]types.ValidationWeight)
	totalWeight := int64(0)
	for _, weight := range epochData.ValidationWeights {
		if weight == nil {
			k.LogError("Validation weight is nil", types.Claims, "height", pocStartHeight, "modelId", modelId)
			continue
		}

		totalWeight += weight.Weight
		weightMap[weight.MemberAddress] = *weight
	}

	k.LogInfo("Epoch group weight data", types.Claims, "height", pocStartHeight, "modelId", modelId, "totalWeight", totalWeight)

	return &epochData, weightMap, totalWeight, true
}

func (k msgServer) getMustBeValidatedInferences(ctx sdk.Context, msg *types.MsgClaimRewards) ([]string, error) {
	// Get the main epoch data
	mainEpochData, mainWeightMap, mainTotalWeight, found := k.getEpochGroupWeightData(ctx, msg.PocStartHeight, "")
	if !found {
		return nil, types.ErrCurrentEpochGroupNotFound
	}

	epoch, found := k.GetEpoch(ctx, mainEpochData.EpochId)
	if !found || epoch == nil {
		k.LogError("MsgClaimReward. getMustBeValidatedInferences. Epoch not found", types.Claims,
			"epochId", mainEpochData.EpochId, "found", found, "epoch", epoch)
		return nil, types.ErrEpochNotFound.Wrapf("epochId = %d. found = %v. epoch = %v", mainEpochData.EpochId, found, epoch)
	}

	if uint64(epoch.PocStartBlockHeight) != msg.PocStartHeight || uint64(epoch.PocStartBlockHeight) != mainEpochData.PocStartBlockHeight {
		k.LogError("MsgClaimReward. getMustBeValidatedInferences. ILLEGAL STATE. Epoch start block height does not match", types.Claims,
			"epoch.PocStartHeight", epoch.PocStartBlockHeight, "msg.PocStartHeight", msg.PocStartHeight, "mainEpochData.PocStartBlockHeight", mainEpochData.PocStartBlockHeight)
		return nil, types.ErrIllegalState.Wrapf("epoch.PocStartHeight = %d, msg.PocStartHeight = %d, mainEpochData.PocStartBlockHeight = %d", epoch.PocStartBlockHeight, msg.PocStartHeight, mainEpochData.PocStartBlockHeight)
	}

	params := k.Keeper.GetParams(ctx).EpochParams
	epochContext := types.NewEpochContext(*epoch, *params)

	// Create a map to store weight maps for each model
	modelWeightMaps := make(map[string]map[string]types.ValidationWeight)
	modelTotalWeights := make(map[string]int64)

	// Store main model data
	modelWeightMaps[""] = mainWeightMap
	modelTotalWeights[""] = mainTotalWeight

	// Check if validator is in the main weight map
	_, found = mainWeightMap[msg.Creator]
	if !found {
		k.LogError("Validator not found in main weight map", types.Claims, "validator", msg.Creator)
		return nil, types.ErrParticipantNotFound
	}

	// Get sub models from the main epoch data
	for _, subModelId := range mainEpochData.SubGroupModels {
		_, subWeightMap, subTotalWeight, found := k.getEpochGroupWeightData(ctx, msg.PocStartHeight, subModelId)
		if !found {
			k.LogWarn("Sub epoch data not found", types.Claims, "height", msg.PocStartHeight, "modelId", subModelId)
			continue
		}

		modelWeightMaps[subModelId] = subWeightMap
		modelTotalWeights[subModelId] = subTotalWeight
	}

	mustBeValidated := make([]string, 0)
	finishedInferences := k.GetInferenceValidationDetailsForEpoch(ctx, mainEpochData.EpochGroupId)
	var skipped int
	for _, inference := range finishedInferences {
		if inference.ExecutorId == msg.Creator {
			continue
		}

		// Determine which model this inference belongs to
		modelId := inference.Model
		weightMap, exists := modelWeightMaps[modelId]
		if !exists {
			return nil, types.ErrInferenceHasInvalidModel
		}

		// Check if validator is in the weight map for this model
		validatorPowerForModel, found := weightMap[msg.Creator]
		if !found {
			k.LogInfo("Validator not found in weight map for model", types.Claims, "validator", msg.Creator, "model", modelId)
			continue
		}

		// Check if executor is in the weight map for this model
		executorPower, found := weightMap[inference.ExecutorId]
		if !found {
			k.LogWarn("Executor not found in weight map", types.Claims, "executor", inference.ExecutorId, "model", modelId)
			continue
		}

		// Get the total weight for this model
		totalWeight := modelTotalWeights[modelId]

		if k.OverlapsWithPoC(&inference, epochContext) && !k.isActiveDuringPoC(&validatorPowerForModel) {
			skipped++
			continue
		}

		k.LogInfo("Getting validation", types.Claims, "seed", msg.Seed, "totalWeight", totalWeight, "executorPower", executorPower, "validatorPower", validatorPowerForModel)
		shouldValidate, s := calculations.ShouldValidate(msg.Seed, &inference, uint32(totalWeight), uint32(validatorPowerForModel.Weight), uint32(executorPower.Weight),
			k.Keeper.GetParams(ctx).ValidationParams)
		k.LogInfo(s, types.Claims, "inference", inference.InferenceId, "seed", msg.Seed, "model", modelId, "validator", msg.Creator)
		if shouldValidate {
			mustBeValidated = append(mustBeValidated, inference.InferenceId)
		}
	}

	k.LogInfo("Must be validated inferences", types.Claims,
		"count", len(mustBeValidated),
		"validator_not_available_at_poc_skipped", skipped,
		"total", len(finishedInferences),
	)

	return mustBeValidated, nil
}

func (k msgServer) OverlapsWithPoC(inferenceDetails *types.InferenceValidationDetails, epochContext types.EpochContext) bool {
	if inferenceDetails == nil {
		k.LogError("MsgClaimReward. OverlapsWithPoC. Inference details is nil", types.Claims, "inferenceDetails", inferenceDetails)
		return false
	}

	if inferenceDetails.CreatedAtBlockHeight == 0 {
		k.LogWarn("MsgClaimReward. OverlapsWithPoC. CreatedAtBlockHeight is not set", types.Claims, "inferenceDetails", inferenceDetails)
		return false
	} else if inferenceDetails.CreatedAtBlockHeight < 0 {
		k.LogError("MsgClaimReward. OverlapsWithPoC. CreatedAtBlockHeight is negative!", types.Claims, "inferenceDetails", inferenceDetails)
		return false
	}

	happenedAfterCutoff := inferenceDetails.CreatedAtBlockHeight >= epochContext.InferenceValidationCutoff()
	return happenedAfterCutoff
}

func (k msgServer) isActiveDuringPoC(weight *types.ValidationWeight) bool {
	if weight == nil {
		k.LogError("MsgClaimReward. isActiveDuringPoC. Validation weight is nil", types.Claims, "weight", weight)
		return false
	}

	for _, n := range weight.MlNodes {
		if n.IsActiveDuringPoC() {
			return true
		}
	}

	return false
}
