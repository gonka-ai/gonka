package keeper

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) ClaimRewards(goCtx context.Context, msg *types.MsgClaimRewards) (*types.MsgClaimRewardsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	settleAmount, response := k.validateRequest(ctx, msg)
	if response != nil {
		return response, nil
	}

	err := k.validateClaim(ctx, msg, settleAmount)
	if err != nil {
		return nil, err
	}
	k.LogDebug("Claim verified", types.Claims, "account", msg.Creator, "seed", msg.Seed)

	err = k.payoutClaim(ctx, msg, settleAmount)
	if err != nil {
		return nil, err
	}

	return &types.MsgClaimRewardsResponse{
		Amount: settleAmount.GetTotalCoins(),
		Result: "Rewards claimed",
	}, nil
}

func (ms msgServer) payoutClaim(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount *types.SettleAmount) error {
	ms.LogInfo("Issuing rewards", types.Claims, "address", msg.Creator, "amount", settleAmount.GetTotalCoins())
	escrowPayment := settleAmount.GetWorkCoins()
	err := ms.PayParticipantFromEscrow(ctx, msg.Creator, escrowPayment, "work_coins:"+settleAmount.Participant)
	if err != nil {
		ms.LogError("Error paying participant", types.Claims, "error", err)
		return err
	}
	ms.AddTokenomicsData(ctx, &types.TokenomicsData{TotalFees: settleAmount.GetWorkCoins()})
	err = ms.PayParticipantFromModule(ctx, msg.Creator, settleAmount.GetRewardCoins(), types.ModuleName, "reward_coins:"+settleAmount.Participant)
	if err != nil {
		ms.LogError("Error paying participant for rewards", types.Claims, "error", err)
		return err
	}
	ms.RemoveSettleAmount(ctx, msg.Creator)
	perfSummary, found := ms.GetEpochPerformanceSummary(ctx, settleAmount.PocStartHeight, msg.Creator)
	if found {
		perfSummary.Claimed = true
		ms.SetEpochPerformanceSummary(ctx, perfSummary)
	}
	return nil
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

func (k msgServer) validateClaim(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount *types.SettleAmount) error {
	k.LogInfo("Validating claim", types.Claims, "account", msg.Creator, "seed", msg.Seed, "height", msg.PocStartHeight)
	err := k.validateSeedSignature(ctx, msg, settleAmount)
	if err != nil {
		return err
	}

	mustBeValidated, err := k.getMustBeValidatedInferences(ctx, msg)
	if err != nil {
		return err
	}
	wasValidated := k.getValidatedInferences(ctx, msg)

	validationMissed := false

	for _, inferenceId := range mustBeValidated {
		if !wasValidated[inferenceId] {
			k.LogError("Inference not validated", types.Claims, "inference", inferenceId, "account", msg.Creator)
			validationMissed = true
		}
	}
	if validationMissed {
		return types.ErrValidationsMissed
	}

	return nil
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

func (k msgServer) getEpochGroupWeightData(ctx sdk.Context, pocStartHeight uint64, modelId string) (*types.EpochGroupData, map[string]int64, int64, bool) {
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
	weightMap := make(map[string]int64)
	totalWeight := int64(0)
	for _, weight := range epochData.ValidationWeights {
		totalWeight += weight.Weight
		weightMap[weight.MemberAddress] = weight.Weight
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

	// Create a map to store weight maps for each model
	modelWeightMaps := make(map[string]map[string]int64)
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
			continue
		}

		modelWeightMaps[subModelId] = subWeightMap
		modelTotalWeights[subModelId] = subTotalWeight
	}

	mustBeValidated := make([]string, 0)
	finishedInferences := k.GetInferenceValidationDetailsForEpoch(ctx, mainEpochData.EpochGroupId)
	for _, inference := range finishedInferences {
		if inference.ExecutorId == msg.Creator {
			continue
		}

		// Determine which model this inference belongs to
		modelId := inference.Model
		weightMap, exists := modelWeightMaps[modelId]
		if !exists {
			// If no specific model weight map exists, use the main one
			weightMap = mainWeightMap
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

		k.LogInfo("Getting validation", types.Claims, "seed", msg.Seed, "totalWeight", totalWeight, "executorPower", executorPower, "validatorPower", validatorPowerForModel)
		shouldValidate, s := calculations.ShouldValidate(msg.Seed, &inference, uint32(totalWeight), uint32(validatorPowerForModel), uint32(executorPower),
			k.Keeper.GetParams(ctx).ValidationParams)
		k.LogInfo(s, types.Claims, "inference", inference.InferenceId, "seed", msg.Seed, "model", modelId, "validator", msg.Creator)
		if shouldValidate {
			mustBeValidated = append(mustBeValidated, inference.InferenceId)
		}
	}
	return mustBeValidated, nil
}
