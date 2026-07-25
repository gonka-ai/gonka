package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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
	if err := k.CheckPermission(goCtx, msg, ActiveParticipantPermission, PreviousActiveParticipantPermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("Bridge exchange: Processing transaction request", types.Messages,
		"validator", msg.Validator,
		"originChain", msg.OriginChain,
		"blockNumber", msg.BlockNumber,
		"receiptIndex", msg.ReceiptIndex)

	validated, err := k.ValidateBridgeExchange(ctx, msg)
	if err != nil {
		return nil, err
	}

	if !validated.IsCreate {
		existingTx := validated.ExistingTx
		validatorPower := validated.ValidatorPower
		totalEpochPower := validated.TotalEpochPower
		addr := validated.ValidatorAddress

		// Record this validator's confirmation in its own sub-key. Write
		// cost is constant regardless of how many prior validators have
		// already confirmed, which is the whole point of the split.
		if err := k.AddBridgeTransactionValidator(ctx, existingTx, addr.String()); err != nil {
			k.LogError("Bridge exchange: Failed to record validator confirmation", types.Messages,
				"validator", msg.Validator, "error", err)
			return nil, fmt.Errorf("failed to record validator confirmation: %v", err)
		}
		existingTx.TotalValidationPower += validatorPower
		// Clear the rehydrated Validators slice before SetBridgeTransaction
		// so the sync loop there doesn't redundantly re-Set every prior
		// validator's sub-key on each confirmation.
		existingTx.Validators = nil

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

				// Handle token minting / native release for completed transaction
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
	proposedTx := validated.ProposedTx
	addr := validated.ValidatorAddress
	validatorPower := validated.ValidatorPower

	proposedTx.Id = "" // Will be set by SetBridgeTransaction
	proposedTx.Status = types.BridgeTransactionStatus_BRIDGE_PENDING
	proposedTx.EpochIndex = validated.EpochIndex
	proposedTx.TotalValidationPower = validatorPower
	// Record the first validator's confirmation via the KeySet and leave
	// the in-memory Validators slice empty so the base struct stays
	// constant-size on disk. Store normalized (canonical lowercase)
	// address to ensure consistency.
	if err := k.AddBridgeTransactionValidator(ctx, proposedTx, addr.String()); err != nil {
		k.LogError("Bridge exchange: Failed to record first validator confirmation", types.Messages,
			"validator", msg.Validator, "error", err)
		return nil, fmt.Errorf("failed to record validator confirmation: %v", err)
	}
	proposedTx.Validators = nil

	k.SetBridgeTransaction(ctx, proposedTx)

	k.LogInfo("Bridge exchange: New transaction created",
		types.Messages,
		"chainId", msg.OriginChain,
		"blockNumber", msg.BlockNumber,
		"receiptIndex", msg.ReceiptIndex,
		"validator", msg.Validator,
		"validatorPower", validatorPower,
		"epochIndex", validated.EpochIndex,
		"amount", msg.Amount,
		"uniqueId", proposedTx.Id)

	return &types.MsgBridgeExchangeResponse{
		Id: proposedTx.Id,
	}, nil
}
