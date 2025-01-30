package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
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
	k.LogDebug("Claim verified", "account", msg.Creator, "seed", msg.Seed)

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
	ms.LogInfo("Issuing rewards", "address", msg.Creator, "amount", settleAmount.GetTotalCoins())
	escrowPayment := settleAmount.GetRefundCoins() + settleAmount.GetWorkCoins()
	err := ms.PayParticipantFromEscrow(ctx, msg.Creator, escrowPayment)
	if err != nil {
		ms.LogError("Error paying participant", "error", err)
		return err
	}
	err = ms.PayParticipantFromModule(ctx, msg.Creator, settleAmount.GetRewardCoins(), types.StandardRewardPoolAccName)
	if err != nil {
		ms.LogError("Error paying participant for rewards", "error", err)
		return err
	}
	ms.RemoveSettleAmount(ctx, msg.Creator)
	return nil
}

func (k msgServer) validateRequest(ctx sdk.Context, msg *types.MsgClaimRewards) (*types.SettleAmount, *types.MsgClaimRewardsResponse) {
	settleAmount, found := k.GetSettleAmount(ctx, msg.Creator)
	if !found {
		k.LogDebug("SettleAmount not found for address", "address", msg.Creator)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this address",
		}
	}
	if settleAmount.PocStartHeight != msg.PocStartHeight {
		k.LogDebug("SettleAmount does not match height", "height", msg.PocStartHeight, "settleHeight", settleAmount.PocStartHeight)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this block height",
		}
	}
	if settleAmount.GetTotalCoins() == 0 {
		k.LogDebug("SettleAmount had zero coins", "address", msg.Creator)
		return nil, &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this address",
		}
	}

	return &settleAmount, nil
}

func (k msgServer) validateClaim(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount *types.SettleAmount) error {
	k.LogInfo("Validating claim", "account", msg.Creator, "seed", msg.Seed, "height", msg.PocStartHeight)
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
			k.LogError("Inference not validated", "inference", inferenceId, "account", msg.Creator)
			validationMissed = true
		}
	}
	if validationMissed {
		return types.ErrValidationsMissed
	}

	return nil
}

func (ms msgServer) validateSeedSignature(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount *types.SettleAmount) error {
	ms.LogDebug("Validating seed signature", "account", msg.Creator, "seed", msg.Seed, "height", msg.PocStartHeight)
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return types.ErrPocAddressInvalid
	}
	acc := ms.AccountKeeper.GetAccount(ctx, addr)
	if acc == nil {
		ms.LogError("Account not found for signature", "address", msg.Creator)
		return types.ErrParticipantNotFound
	}
	pubKey := acc.GetPubKey()
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(msg.Seed))
	signature, err := hex.DecodeString(settleAmount.SeedSignature)
	if err != nil {
		ms.LogError("Error decoding signature", "error", err)
		return err
	}
	ms.LogDebug("Verifying signature", "seedBytes", hex.EncodeToString(seedBytes), "signature", hex.EncodeToString(signature), "pubkey", pubKey.String())
	if !pubKey.VerifySignature(seedBytes, signature) {
		ms.LogError("Signature verification failed", "seed", msg.Seed, "signature", settleAmount.SeedSignature, "seedBytes", hex.EncodeToString(seedBytes))
		return types.ErrClaimSignatureInvalid
	}
	return nil
}

func (k msgServer) getValidatedInferences(ctx sdk.Context, msg *types.MsgClaimRewards) map[string]bool {
	wasValidatedRaw, found := k.GetEpochGroupValidations(ctx, msg.Creator, msg.PocStartHeight)
	if !found {
		k.LogInfo("Validations not found", "height", msg.PocStartHeight, "account", msg.Creator)
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

func (k msgServer) getMustBeValidatedInferences(ctx sdk.Context, msg *types.MsgClaimRewards) ([]string, error) {
	epochData, found := k.GetEpochGroupData(ctx, msg.PocStartHeight)
	if !found {
		k.LogError("Epoch data not found", "height", msg.PocStartHeight)
		return nil, types.ErrCurrentEpochGroupNotFound
	}

	totalWeight := int64(0)
	weightMap := make(map[string]int64)
	for _, weight := range epochData.ValidationWeights {
		totalWeight += weight.Weight
		weightMap[weight.MemberAddress] = weight.Weight
	}
	validatorPower, found := weightMap[msg.Creator]
	if !found {
		k.LogError("Validator not found in weight map", "validator", msg.Creator)
		return nil, types.ErrParticipantNotFound
	}
	mustBeValidated := make([]string, 0)
	for _, inference := range epochData.FinishedInferences {
		if inference.Executor == msg.Creator {
			continue
		}
		executorPower, found := weightMap[inference.Executor]
		if !found {
			k.LogWarn("Executor not found in weight map", "executor", inference.Executor)
			continue
		}
		shouldValidate, s := ShouldValidate(msg.Seed, inference, uint32(totalWeight), uint32(validatorPower), uint32(executorPower),
			k.Keeper.GetParams(ctx).ValidationParams)
		k.LogDebug("ValidationDecision", "text", s, "inference", inference.InferenceId, "seed", msg.Seed)
		if shouldValidate {
			mustBeValidated = append(mustBeValidated, inference.InferenceId)
		}
	}
	return mustBeValidated, nil
}

func ShouldValidate(
	seed int64,
	inferenceDetails *types.InferenceDetail,
	totalPower uint32,
	validatorPower uint32,
	executorPower uint32,
	validationParams *types.ValidationParams,
) (bool, string) {
	rangeSize := validationParams.MaxValidationAverage - validationParams.MinValidationAverage
	executorAdjustment := rangeSize * (1 - float64(inferenceDetails.ExecutorReputation))
	// 100% rep will be 0, 0% rep will be rangeSize
	targetValidations := validationParams.MinValidationAverage + executorAdjustment
	ourProbability := float32(targetValidations) * (float32(validatorPower)) / float32(totalPower-executorPower)
	if ourProbability > 1 {
		ourProbability = 1
	}
	randFloat := deterministicFloat(seed, inferenceDetails.InferenceId)
	shouldValidate := randFloat < float64(ourProbability)
	return shouldValidate, fmt.Sprintf(
		"Should Validate: %v randFloat: %v ourProbability: %v, rangeSize: %v, executorAdjustment: %v, targetValidations: %v",
		shouldValidate, randFloat, ourProbability, rangeSize, executorAdjustment, targetValidations,
	)
}

// In lieu of a real random number generator, we use a deterministic function that takes a seed and an inferenceId
// This is more or less as random as using a seed in a deterministic random determined by this same hash, and has
// the advantage of being 100% deterministic regardless of platform and also faster to compute.
func deterministicFloat(seed int64, inferenceId string) float64 {
	// Concatenate the seed and inferenceId into a single string
	input := fmt.Sprintf("%d:%s", seed, inferenceId)

	// Use a cryptographic hash (e.g., SHA-256)
	h := sha256.New()
	h.Write([]byte(input))
	hash := h.Sum(nil)

	// Convert the first 8 bytes of the hash into a uint64
	hashInt := binary.BigEndian.Uint64(hash[:8])

	// Normalize the uint64 value to a float64 in the range [0, 1)
	return float64(hashInt) / float64(^uint64(0)) // ^uint64(0) gives max uint64
}
