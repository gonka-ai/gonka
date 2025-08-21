package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func PubKeyToAddress(pubKey string) (string, error) {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(pubKeyBytes)
	valAddr := hash[:20]
	return strings.ToUpper(hex.EncodeToString(valAddr)), nil
}

func (k msgServer) BridgeExchange(goCtx context.Context, msg *types.MsgBridgeExchange) (*types.MsgBridgeExchangeResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("Bridge exchange: Processing transaction request", types.Messages,
		"validator", msg.Validator,
		"originChain", msg.OriginChain,
		"blockNumber", msg.BlockNumber,
		"receiptIndex", msg.ReceiptIndex)

	// Parse the amount to ensure it's valid
	_, ok := new(big.Int).SetString(msg.Amount, 10)
	if !ok {
		k.LogError("Bridge exchange: Invalid amount", types.Messages, "amount", msg.Amount)
		return nil, fmt.Errorf("invalid amount: %s", msg.Amount)
	}

	// Get the account address
	addr, err := sdk.AccAddressFromBech32(msg.Validator)
	if err != nil {
		k.LogError(
			"Bridge exchange: failed to decode bech32 address",
			types.Messages,
			"error", err.Error())
		return nil, fmt.Errorf("invalid validator address: %v", err)
	}

	// Check if the validator account exists
	acc := k.AccountKeeper.GetAccount(ctx, addr)
	if acc == nil {
		k.LogError("Bridge exchange: Account not found for validator", types.Messages, "validator", msg.Validator)
		return nil, fmt.Errorf("account not found for validator")
	}

	// Create transaction object with all the content for secure validation
	proposedTx := &types.BridgeTransaction{
		ChainId:         msg.OriginChain,
		ContractAddress: msg.ContractAddress,
		OwnerAddress:    msg.OwnerAddress,
		Amount:          msg.Amount,
		BlockNumber:     msg.BlockNumber,
		ReceiptIndex:    msg.ReceiptIndex,
		ReceiptsRoot:    msg.ReceiptsRoot,
		// Status and other fields will be set later
	}

	// Check if this exact transaction content has already been processed
	existingTx, found := k.GetBridgeTransactionByContent(ctx, proposedTx)
	if found {
		// Validate that the existing transaction has identical content (double-check security)
		if !bridgeTransactionsEqual(existingTx, proposedTx) {
			k.LogError("Bridge exchange: Content mismatch for existing transaction", types.Messages,
				"existingChainId", existingTx.ChainId,
				"proposedChainId", proposedTx.ChainId,
				"existingContract", existingTx.ContractAddress,
				"proposedContract", proposedTx.ContractAddress,
				"existingOwner", existingTx.OwnerAddress,
				"proposedOwner", proposedTx.OwnerAddress,
				"existingAmount", existingTx.Amount,
				"proposedAmount", proposedTx.Amount)
			return nil, fmt.Errorf("transaction content mismatch - potential attack detected")
		}
		// Get the epoch group for the existing transaction using epochIndex
		epochGroup, err := k.GetEpochGroup(goCtx, existingTx.EpochIndex, "")
		if err != nil {
			k.LogError("Bridge exchange: unable to get epoch group for existing transaction", types.Messages,
				"epochIndex", existingTx.EpochIndex, "error", err)
			return nil, fmt.Errorf("unable to get epoch group for existing transaction: %v", err)
		}

		// Get epoch group members directly
		epochGroupMembers, err := epochGroup.GetGroupMembers(ctx)
		if err != nil {
			k.LogError("Bridge exchange: unable to get epoch group members", types.Messages,
				"epochIndex", existingTx.EpochIndex, "error", err)
			return nil, fmt.Errorf("unable to get epoch group members: %v", err)
		}

		// Check if validator is in the epoch group
		isInEpochGroup := false
		var validatorPower int64
		for _, member := range epochGroupMembers {
			memberAddr, err := sdk.AccAddressFromBech32(member.Member.Address)
			if err != nil {
				continue
			}
			if memberAddr.Equals(addr) {
				isInEpochGroup = true
				// Parse weight from string (group module stores weight as string)
				weight, err := strconv.ParseInt(member.Member.Weight, 10, 64)
				if err != nil {
					k.LogError("Bridge exchange: unable to parse member weight", types.Messages,
						"member", member.Member.Address, "weight", member.Member.Weight, "error", err)
					continue
				}
				validatorPower = weight
				break
			}
		}

		if !isInEpochGroup {
			k.LogError("Bridge exchange: Validator not in transaction's epoch group", types.Messages,
				"validator", msg.Validator, "epochIndex", existingTx.EpochIndex)
			return nil, fmt.Errorf("validator not in transaction's epoch group")
		}

		// Check if validator already validated
		for _, validator := range existingTx.Validators {
			if validator == msg.Validator {
				k.LogError("Bridge exchange: Validator has already validated this transaction", types.Messages, "validator", msg.Validator)
				return nil, fmt.Errorf("validator has already validated this transaction")
			}
		}

		// Add validator and their power to totals
		existingTx.Validators = append(existingTx.Validators, msg.Validator)
		existingTx.TotalValidationPower += validatorPower

		// Use total epoch power from epoch group data
		totalEpochPower := epochGroup.GroupData.TotalWeight

		k.LogInfo("Bridge exchange: Additional validator added",
			types.Messages,
			"originChain", msg.OriginChain,
			"blockNumber", msg.BlockNumber,
			"receiptIndex", msg.ReceiptIndex,
			"validator", msg.Validator,
			"validatorPower", validatorPower,
			"totalValidationPower", existingTx.TotalValidationPower,
			"totalEpochPower", totalEpochPower,
			"status", existingTx.Status)

		// Check if we have majority (50+% of total power)
		requiredPower := (totalEpochPower / 2) + 1

		if existingTx.TotalValidationPower >= requiredPower {
			// Only process completion once to avoid duplicate mints
			if existingTx.Status == types.BridgeTransactionStatus_BRIDGE_PENDING {
				existingTx.Status = types.BridgeTransactionStatus_BRIDGE_COMPLETED
				k.SetBridgeTransaction(ctx, existingTx)

				// Handle token minting for completed transaction
				if err := k.handleCompletedBridgeTransaction(ctx, existingTx); err != nil {
					k.LogError("Bridge exchange: Failed to handle completed bridge transaction",
						types.Messages,
						"error", err,
						"originChain", msg.OriginChain,
						"blockNumber", msg.BlockNumber,
						"receiptIndex", msg.ReceiptIndex)
					return nil, err
				}

				k.LogInfo("Bridge exchange: transaction reached majority validation",
					types.Messages,
					"originChain", msg.OriginChain,
					"blockNumber", msg.BlockNumber,
					"receiptIndex", msg.ReceiptIndex,
					"powerRequired", requiredPower,
					"powerReceived", existingTx.TotalValidationPower,
					"totalEpochPower", totalEpochPower)

				return &types.MsgBridgeExchangeResponse{
					Id: existingTx.Id,
				}, nil
			}
		} else {
			k.LogInfo("Bridge exchange: transaction pending majority validation",
				types.Messages,
				"originChain", msg.OriginChain,
				"blockNumber", msg.BlockNumber,
				"receiptIndex", msg.ReceiptIndex,
				"powerRequired", requiredPower,
				"powerReceived", existingTx.TotalValidationPower,
				"totalEpochPower", totalEpochPower)
		}

		k.SetBridgeTransaction(ctx, existingTx)
		return &types.MsgBridgeExchangeResponse{
			Id: existingTx.Id,
		}, nil
	}

	// Transaction doesn't exist, create new one
	// Get current epoch group
	currentEpochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		k.LogError("Bridge exchange: unable to get current epoch group", types.Messages, "error", err)
		return nil, fmt.Errorf("unable to get current epoch group: %v", err)
	}

	// Get current epoch group members directly
	currentEpochMembers, err := currentEpochGroup.GetGroupMembers(ctx)
	if err != nil {
		k.LogError("Bridge exchange: unable to get current epoch group members", types.Messages,
			"epochIndex", currentEpochGroup.GroupData.EpochIndex, "error", err)
		return nil, fmt.Errorf("unable to get current epoch group members: %v", err)
	}

	// Check if validator is in current epoch group
	isActive := false
	var validatorPower int64
	for _, member := range currentEpochMembers {
		memberAddr, err := sdk.AccAddressFromBech32(member.Member.Address)
		if err != nil {
			continue
		}
		if memberAddr.Equals(addr) {
			isActive = true
			// Parse weight from string (group module stores weight as string)
			weight, err := strconv.ParseInt(member.Member.Weight, 10, 64)
			if err != nil {
				k.LogError("Bridge exchange: unable to parse member weight", types.Messages,
					"member", member.Member.Address, "weight", member.Member.Weight, "error", err)
				continue
			}
			validatorPower = weight
			break
		}
	}

	if !isActive {
		k.LogError("Bridge exchange: Validator not in active participants", types.Messages, "validator", msg.Validator)
		return nil, fmt.Errorf("validator not in active participants")
	}

	// Complete the proposed transaction with epoch and validation data
	proposedTx.Id = "" // Will be set by SetBridgeTransaction
	proposedTx.Status = types.BridgeTransactionStatus_BRIDGE_PENDING
	proposedTx.EpochIndex = currentEpochGroup.GroupData.EpochIndex
	proposedTx.Validators = []string{msg.Validator}
	proposedTx.TotalValidationPower = validatorPower

	k.SetBridgeTransaction(ctx, proposedTx)

	k.LogInfo("Bridge exchange: New transaction created",
		types.Messages,
		"chainId", msg.OriginChain,
		"blockNumber", msg.BlockNumber,
		"receiptIndex", msg.ReceiptIndex,
		"validator", msg.Validator,
		"validatorPower", validatorPower,
		"epochIndex", currentEpochGroup.GroupData.EpochIndex,
		"amount", msg.Amount,
		"uniqueId", proposedTx.Id)

	return &types.MsgBridgeExchangeResponse{
		Id: proposedTx.Id,
	}, nil
}
