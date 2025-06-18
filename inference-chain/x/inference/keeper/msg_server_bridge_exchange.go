package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
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
		k.LogError("Invalid amount", types.Messages, "amount", msg.Amount)
		return nil, fmt.Errorf("invalid amount: %s", msg.Amount)
	}

	// Get current epoch group and active participants
	currentEpochGroup, error := k.Keeper.GetCurrentEpochGroup(goCtx)
	if error != nil {
		k.LogError(
			"Bridge exchange: unable to get current epoch group",
			types.Messages,
			"error", error)
		return nil, fmt.Errorf("unable to get current epoch group: %v", error)
	}

	activeParticipants, found := k.GetActiveParticipants(ctx, currentEpochGroup.GroupData.EpochGroupId)

	if !found {
		return nil, fmt.Errorf("no active participants found for current epoch")
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

	// Check if the validator is in active participants by checking their account
	acc := k.AccountKeeper.GetAccount(ctx, addr)
	if acc == nil {
		k.LogError("Bridge exchange: Account not found for validator", types.Messages, "validator", msg.Validator)
		return nil, fmt.Errorf("account not found for validator")
	}

	isActive := false
	for _, participant := range activeParticipants.Participants {
		// Get the account associated with this participant
		participantAddr, err := sdk.AccAddressFromBech32(participant.Index)
		if err != nil {
			continue
		}

		if participantAddr.Equals(addr) {
			isActive = true
			break
		}
	}

	if !isActive {
		k.LogError("Bridge exchange: Validator not in active participants", types.Messages, "validator", msg.Validator)
		return nil, fmt.Errorf("validator not in active participants")
	}

	// Check if this transaction has already been processed
	existingTx, found := k.GetBridgeTransaction(ctx, msg.OriginChain, msg.BlockNumber, msg.ReceiptIndex)
	if found {
		// If exists, check if validator already validated
		for _, validator := range existingTx.Validators {
			if validator == msg.Validator {
				return nil, fmt.Errorf("validator has already validated this transaction")
			}
		}

		// Add validator
		existingTx.Validators = append(existingTx.Validators, msg.Validator)
		existingTx.ValidationCount++

		k.LogInfo("Bridge exchange: Additional validator added",
			types.Messages,
			"originChain", msg.OriginChain,
			"blockNumber", msg.BlockNumber,
			"receiptIndex", msg.ReceiptIndex,
			"validator", msg.Validator,
			"currentValidations", existingTx.ValidationCount)

		// Check if we have majority
		requiredValidators := (len(activeParticipants.Participants) * 2) / 3

		if existingTx.ValidationCount >= uint32(requiredValidators) {
			existingTx.Status = types.BridgeTransactionStatus_BRIDGE_COMPLETED

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
				"validationsRequired", requiredValidators,
				"validationsReceived", existingTx.ValidationCount,
				"totalValidators", len(activeParticipants.Participants))
		} else {
			k.LogInfo("Bridge exchange: transaction pending majority validation",
				types.Messages,
				"originChain", msg.OriginChain,
				"blockNumber", msg.BlockNumber,
				"receiptIndex", msg.ReceiptIndex,
				"validationsRequired", requiredValidators,
				"validationsReceived", existingTx.ValidationCount,
				"totalValidators", len(activeParticipants.Participants))
		}

		k.SetBridgeTransaction(ctx, existingTx)
		return &types.MsgBridgeExchangeResponse{
			Id: existingTx.Id,
		}, nil
	}

	// Create new bridge transaction
	bridgeTx := &types.BridgeTransaction{
		Id:              "", // Will be set by SetBridgeTransaction
		OriginChain:     msg.OriginChain,
		ContractAddress: msg.ContractAddress,
		OwnerAddress:    msg.OwnerAddress,
		Amount:          msg.Amount,
		Recipient:       msg.OwnerAddress, // The original owner should receive the bridged tokens
		BlockHeight:     ctx.BlockHeight(),
		Timestamp:       ctx.BlockTime().Unix(),
		Status:          types.BridgeTransactionStatus_BRIDGE_PENDING,
		Validators:      []string{msg.Validator},
		ValidationCount: 1,
		BlockNumber:     msg.BlockNumber,
		ReceiptIndex:    msg.ReceiptIndex,
		ReceiptsRoot:    msg.ReceiptsRoot,
	}
	k.SetBridgeTransaction(ctx, bridgeTx)

	k.LogInfo("Bridge exchange: New transaction created",
		types.Messages,
		"originChain", msg.OriginChain,
		"blockNumber", msg.BlockNumber,
		"receiptIndex", msg.ReceiptIndex,
		"validator", msg.Validator,
		"amount", msg.Amount,
		"uniqueId", bridgeTx.Id)

	return &types.MsgBridgeExchangeResponse{
		Id: bridgeTx.Id,
	}, nil
}
