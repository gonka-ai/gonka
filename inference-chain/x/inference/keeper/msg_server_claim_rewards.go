package keeper

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

const maxInferenceSampleSize = 10000

func (k msgServer) ClaimRewards(goCtx context.Context, msg *types.MsgClaimRewards) (*types.MsgClaimRewardsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	settleAmount, response := k.validateRequest(ctx, msg)
	if response != nil {
		k.LogInfo("Validate request failed", types.Claims, "error", response.Result, "account", msg.Creator)
		return response, nil
	}
	k.LogInfo("Validate request succeeded", types.Claims, "account", msg.Creator, "settleAmount", settleAmount)

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
	// TODO: Optimization: Payout claim should be done in one transaction
	ms.LogInfo("Issuing rewards", types.Claims, "address", msg.Creator, "amount", settleAmount.GetTotalCoins())

	// Pay for work from escrow
	escrowPayment := settleAmount.GetWorkCoins()
	params := ms.GetParams(ctx)
	workVestingPeriod := &params.TokenomicsParams.WorkVestingPeriod
	if err := ms.PayParticipantFromEscrow(ctx, msg.Creator, int64(escrowPayment), "work_coins:"+settleAmount.Participant, workVestingPeriod); err != nil {
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
	rewardVestingPeriod := &params.TokenomicsParams.RewardVestingPeriod
	if err := ms.PayParticipantFromModule(ctx, msg.Creator, int64(settleAmount.GetRewardCoins()), types.ModuleName, "reward_coins:"+settleAmount.Participant, rewardVestingPeriod); err != nil {
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
	// impossible, but check anyhow
	if settleAmount.GetTotalCoins() < 0 {
		return nil, types.ErrNegativeRewardAmount
	}
	return &types.MsgClaimRewardsResponse{
		Amount: uint64(settleAmount.GetTotalCoins()),
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
	_, err := fmt.Sscanf(errMsg, "spendable balance %dnicoin is smaller than %dngonka", &spendable, &required)
	if err != nil {
		return 0, 0
	}
	return spendable, required
}

func (ms msgServer) finishSettle(ctx sdk.Context, settleAmount *types.SettleAmount) {
	ms.RemoveSettleAmount(ctx, settleAmount.Participant)
	perfSummary, found := ms.GetEpochPerformanceSummary(ctx, settleAmount.EpochIndex, settleAmount.Participant)
	if found {
		perfSummary.Claimed = true
		err := ms.SetEpochPerformanceSummary(ctx, perfSummary)
		if err != nil {
			ms.LogError("Error setting epoch performance summary", types.Claims, "error", err)
		}
	}
}

func (k msgServer) validateRequest(ctx sdk.Context, msg *types.MsgClaimRewards) (*types.SettleAmount, *types.MsgClaimRewardsResponse) {
	currentEpoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("GetCurrentEpoch failed", types.Claims, "error", err)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "Can't validate claim, current epoch group not found",
		}
	}

	if (currentEpoch.GroupData.EpochIndex - 1) != msg.EpochIndex {
		k.LogError("Current epoch group does not match previous epoch", types.Claims, "epoch", msg.EpochIndex, "currentEpoch", currentEpoch.GroupData.EpochIndex)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "Can't validate claim, current epoch group does not match previous epoch",
		}
	}
	settleAmount, found := k.GetSettleAmount(ctx, msg.Creator)
	if !found {
		k.LogInfo("SettleAmount not found for address", types.Claims, "address", msg.Creator)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this address",
		}
	}
	if settleAmount.EpochIndex != msg.EpochIndex {
		k.LogWarn("SettleAmount does not match epoch index", types.Claims, "epoch", msg.EpochIndex, "settleEpoch", settleAmount.EpochIndex)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this block height",
		}
	}
	if ctx.BlockHeight()-settleAmount.LastClaimAttempt < 30 {
		k.LogInfo("Claim rate limited", types.Claims, "address", msg.Creator, "lastAttempt", settleAmount.LastClaimAttempt)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "Claim rate limited",
		}
	}
	settleAmount.LastClaimAttempt = ctx.BlockHeight()
	k.SetSettleAmount(ctx, settleAmount)
	if settleAmount.GetTotalCoins() == 0 {
		k.LogInfo("SettleAmount had zero coins", types.Claims, "address", msg.Creator)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this address",
		}
	}

	return &settleAmount, nil
}

func (k msgServer) validateClaim(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount *types.SettleAmount) (*types.MsgClaimRewardsResponse, error) {
	k.LogInfo("Validating claim", types.Claims, "account", msg.Creator, "seed", msg.Seed, "epoch", msg.EpochIndex)

	// Validate the seed signature
	if err := k.validateSeedSignature(ctx, msg, settleAmount); err != nil {
		k.LogError("Seed signature validation failed", types.Claims, "error", err)
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "Seed signature validation failed",
		}, err
	}

	// Check for missed validations
	if validationMissedSignificance, err := k.hasSignificantMissedValidations(ctx, msg); err != nil {
		k.LogError("Failed to check for missed validations", types.Claims, "error", err)
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "Failed to check for missed validations",
		}, err
	} else if validationMissedSignificance {
		k.LogError("Inference validation missed significantly", types.Claims, "account", msg.Creator)
		// TODO: Report that validator has missed validations
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "Inference validation missed significantly",
		}, types.ErrValidationsMissed
	}

	return nil, nil
}

func (k msgServer) hasSignificantMissedValidations(ctx sdk.Context, msg *types.MsgClaimRewards) (bool, error) {
	mustBeValidated, err := k.getMustBeValidatedInferences(ctx, msg)
	if err != nil {
		return false, err
	}
	wasValidated := k.getValidatedInferences(ctx, msg)

	total := len(mustBeValidated)
	missed := 0
	for _, inferenceId := range mustBeValidated {
		if !wasValidated[inferenceId] {
			missed++
		}
	}
	params := k.GetParams(ctx)
	p0 := decimal.NewFromFloat(0.10)
	if params.ValidationParams != nil && params.ValidationParams.BinomTestP0 != nil {
		p0 = params.ValidationParams.BinomTestP0.ToDecimal()
	}
	passed, err := calculations.MissedStatTest(missed, total, p0)
	k.LogInfo("Missed validations", types.Claims, "missed", missed, "totalToBeValidated", total, "passed", passed)

	if err != nil {
		return false, err
	}
	return !passed, nil
}

func (ms msgServer) validateSeedSignatureForPubkey(msg *types.MsgClaimRewards, settleAmount *types.SettleAmount, pubKey cryptotypes.PubKey) error {
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(msg.Seed))
	signature, err := hex.DecodeString(settleAmount.SeedSignature)
	if err != nil {
		ms.LogInfo("Error decoding signature for", types.Claims, "error", err)
		return err
	}
	ms.LogDebug("Verifying signature", types.Claims, "seedBytes", hex.EncodeToString(seedBytes), "signature", hex.EncodeToString(signature), "pubkey", pubKey.String())
	if !pubKey.VerifySignature(seedBytes, signature) {
		return types.ErrClaimSignatureInvalid
	}
	return nil
}

func (ms msgServer) validateSeedSignature(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount *types.SettleAmount) error {
	ms.LogDebug("Validating seed signature", types.Claims, "account", msg.Creator, "seed", msg.Seed, "epoch", msg.EpochIndex)
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return types.ErrPocAddressInvalid
	}
	acc := ms.AccountKeeper.GetAccount(ctx, addr)
	if acc == nil {
		ms.LogError("Account not found for signature", types.Claims, "address", msg.Creator)
		return types.ErrParticipantNotFound
	}
	accountPubkeys, err := ms.GetAccountPubKeysWithGrantees(ctx, msg.Creator)
	if err != nil {
		ms.LogError("Error getting grantees pubkeys", types.Claims, "error", err)
		return err
	}

	for _, granteePubKeyStr := range accountPubkeys {
		pubKey, err := base64.StdEncoding.DecodeString(granteePubKeyStr)
		if err != nil {
			ms.LogError("Error getting grantee pubkey", types.Claims, "error", err)
			continue
		}
		granteePubKey := &secp256k1.PubKey{Key: pubKey}
		err = ms.validateSeedSignatureForPubkey(msg, settleAmount, granteePubKey)
		if err == nil {
			return nil
		}
	}

	ms.LogError("Seed signature validation failed", types.Claims, "account", msg.Creator)
	return types.ErrClaimSignatureInvalid
}

func (k msgServer) getValidatedInferences(ctx sdk.Context, msg *types.MsgClaimRewards) map[string]bool {
	wasValidatedRaw, found := k.GetEpochGroupValidations(ctx, msg.Creator, msg.EpochIndex)
	if !found {
		k.LogInfo("Validations not found", types.Claims, "epoch", msg.EpochIndex, "account", msg.Creator)
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
	mainEpochData, mainWeightMap, mainTotalWeight, found := k.getEpochGroupWeightData(ctx, msg.EpochIndex, "")
	if !found {
		return nil, types.ErrCurrentEpochGroupNotFound
	}

	epoch, found := k.GetEpoch(ctx, mainEpochData.EpochIndex)
	if !found || epoch == nil {
		k.LogError("MsgClaimReward. getMustBeValidatedInferences. Epoch not found", types.Claims,
			"epochId", mainEpochData.EpochIndex, "found", found, "epoch", epoch)
		return nil, types.ErrEpochNotFound.Wrapf("epochId = %d. found = %v. epoch = %v", mainEpochData.EpochIndex, found, epoch)
	}

	if epoch.Index != msg.EpochIndex || epoch.Index != mainEpochData.EpochIndex {
		k.LogError("MsgClaimReward. getMustBeValidatedInferences. ILLEGAL STATE. Epoch start block height does not match", types.Claims,
			"epoch.Index", epoch.Index, "msg.EpochIndex", msg.EpochIndex, "mainEpochData.Index", mainEpochData.EpochIndex)
		return nil, types.ErrIllegalState.Wrapf("epoch.PocStartHeight = %d, msg.EpochIndex = %d, mainEpochData.EpochIndex = %d", epoch.Index, msg.EpochIndex, mainEpochData.EpochIndex)
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
		_, subWeightMap, subTotalWeight, found := k.getEpochGroupWeightData(ctx, msg.EpochIndex, subModelId)
		if !found {
			k.LogWarn("Sub epoch data not found", types.Claims, "epoch", msg.EpochIndex, "modelId", subModelId)
			continue
		}

		modelWeightMaps[subModelId] = subWeightMap
		modelTotalWeights[subModelId] = subTotalWeight
	}

	blockHash := ctx.HeaderInfo().Hash
	blockHashSeed := int64(binary.BigEndian.Uint64(blockHash[:8]))
	rng := rand.New(rand.NewSource(blockHashSeed))

	// Reservoir sampling: iterate all inferences, filter by model, sample filtered items
	sample := make([]types.InferenceValidationDetails, 0, maxInferenceSampleSize)
	totalInferences := 0
	filteredCount := 0

	finishedInferences := k.GetInferenceValidationDetailsForEpoch(ctx, mainEpochData.EpochIndex)
	for _, inference := range finishedInferences {
		totalInferences++

		// Lightweight filters before sampling
		if inference.ExecutorId == msg.Creator {
			continue
		}

		modelId := inference.Model
		if _, exists := modelWeightMaps[modelId]; !exists {
			continue
		}

		if _, found := modelWeightMaps[modelId][msg.Creator]; !found {
			continue
		}

		filteredCount++

		// Reservoir sampling: maintain uniform random sample of filtered items
		if len(sample) < maxInferenceSampleSize {
			sample = append(sample, inference)
		} else {
			j := rng.Intn(filteredCount)
			if j < maxInferenceSampleSize {
				sample[j] = inference
			}
		}
	}

	k.LogInfo("Sampled inferences for validation check", types.Claims,
		"total_inferences", totalInferences,
		"filtered", filteredCount,
		"sampled", len(sample),
		"epoch", mainEpochData.EpochIndex,
	)

	// Run expensive ShouldValidate only on sampled inferences
	skipped := 0
	mustBeValidated := make([]string, 0)
	for _, inference := range sample {
		modelId := inference.Model
		weightMap := modelWeightMaps[modelId]
		validatorPowerForModel := weightMap[msg.Creator]
		executorPower, found := weightMap[inference.ExecutorId]
		if !found {
			k.LogWarn("Executor not found in weight map", types.Claims, "executor", inference.ExecutorId, "model", modelId)
			continue
		}

		totalWeight := modelTotalWeights[modelId]

		if k.OverlapsWithPoC(&inference, epochContext) && !k.isActiveDuringPoC(&validatorPowerForModel) {
			skipped++
			continue
		}

		k.LogDebug("Getting validation", types.Claims, "seed", msg.Seed, "totalWeight", totalWeight, "executorPower", executorPower, "validatorPower", validatorPowerForModel)
		shouldValidate, s := calculations.ShouldValidate(msg.Seed, &inference, uint32(totalWeight), uint32(validatorPowerForModel.Weight), uint32(executorPower.Weight),
			k.Keeper.GetParams(ctx).ValidationParams, false)
		k.LogDebug(s, types.Claims, "inference", inference.InferenceId, "seed", msg.Seed, "model", modelId, "validator", msg.Creator)
		if shouldValidate {
			mustBeValidated = append(mustBeValidated, inference.InferenceId)
		}
	}

	k.LogInfo("Must be validated inferences", types.Claims,
		"count", len(mustBeValidated),
		"validator_not_available_at_poc_skipped", skipped,
		"sampled", len(sample),
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
