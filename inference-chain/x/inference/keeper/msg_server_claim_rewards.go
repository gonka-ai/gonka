package keeper

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"hash/fnv"
	"math/rand"
)

func (k msgServer) ClaimRewards(goCtx context.Context, msg *types.MsgClaimRewards) (*types.MsgClaimRewardsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	settleAmount, found := k.GetSettleAmount(ctx, msg.Creator)
	if !found {
		k.LogDebug("SettleAmount not found for address", "address", msg.Creator)
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this address",
		}, nil
	}
	if settleAmount.PocStartHeight != msg.PocStartHeight {
		k.LogDebug("SettleAmount does not match height", "height", msg.PocStartHeight, "settleHeight", settleAmount.PocStartHeight)
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this block height",
		}, nil
	}
	if settleAmount.GetTotalCoins() == 0 {
		k.LogDebug("SettleAmount had zero coins", "address", msg.Creator)
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this address",
		}, nil
	}

	err := k.validateClaim(ctx, msg, settleAmount)
	if err != nil {
		return nil, err
	}
	k.LogDebug("Claim verified", "account", msg.Creator, "seed", msg.Seed)

	totalCoins := settleAmount.GetTotalCoins()
	k.LogInfo("Issuing rewards", "address", msg.Creator, "amount", totalCoins)
	err = k.PayParticipantFromEscrow(ctx, msg.Creator, totalCoins)
	if err != nil {
		k.LogError("Error paying participant", "error", err)
		// Big question: do we remove the settle amount? Probably not
		return nil, err
	}
	k.RemoveSettleAmount(ctx, msg.Creator)

	return &types.MsgClaimRewardsResponse{
		Amount: totalCoins,
		Result: "Rewards claimed",
	}, nil
}

func (k msgServer) validateClaim(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount types.SettleAmount) error {
	k.LogInfo("Validating claim", "account", msg.Creator, "seed", msg.Seed, "height", msg.PocStartHeight)
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return types.ErrPocAddressInvalid
	}
	acc := k.AccountKeeper.GetAccount(ctx, addr)

	if settleAmount.PocStartHeight != msg.PocStartHeight {
		k.LogError("Claim rewards height mismatch", "expected", settleAmount.PocStartHeight, "actual", msg.PocStartHeight)
		return types.ErrParticipantNotFound
	}
	pubKey := acc.GetPubKey()
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(msg.Seed))
	signature, err := hex.DecodeString(settleAmount.SeedSignature)
	if err != nil {
		k.LogError("Error decoding signature", "error", err)
		return err
	}
	k.LogDebug("Verifying signature", "seedBytes", hex.EncodeToString(seedBytes), "signature", hex.EncodeToString(signature), "pubkey", pubKey.String())
	if !pubKey.VerifySignature(seedBytes, signature) {
		k.LogError("Signature verification failed", "seed", msg.Seed, "signature", settleAmount.SeedSignature, "seedBytes", hex.EncodeToString(seedBytes))
		return types.ErrClaimSignatureInvalid
	}
	epochData, found := k.GetEpochGroupData(ctx, msg.PocStartHeight)
	if !found {
		k.LogError("Epoch data not found", "height", msg.PocStartHeight)
		return types.ErrCurrentEpochGroupNotFound
	}
	mustBeValidated, err := k.getMustBeValidatedInferences(epochData, msg)
	if err != nil {
		return err
	}
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

func (k msgServer) getMustBeValidatedInferences(epochData types.EpochGroupData, msg *types.MsgClaimRewards) ([]string, error) {
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
		if ShouldValidate(msg.Seed, inference, uint32(totalWeight-executorPower), uint32(validatorPower)) {
			mustBeValidated = append(mustBeValidated, inference.InferenceId)
		}
	}
	return mustBeValidated, nil
}

func ShouldValidate(seed int64, inferenceDetails *types.InferenceDetail, totalPower uint32, validatorPower uint32) bool {
	targetValidations := 1 - (inferenceDetails.ExecutorReputation * 0.9)
	ourProbability := targetValidations * (float32(validatorPower) / float32(totalPower))
	inferenceSeed := hashStringToInt64(inferenceDetails.InferenceId)
	randFloat := rand.New(rand.NewSource(seed + inferenceSeed)).Float64()
	return randFloat < float64(ourProbability)
}

func hashStringToInt64(s string) int64 {
	h := fnv.New64a()      // Create a new 64-bit FNV-1a hash
	h.Write([]byte(s))     // Write the string to the hash
	hashValue := h.Sum64() // Get the unsigned 64-bit hash

	// Convert to int64, taking care of potential overflow.
	return int64(hashValue)
}
