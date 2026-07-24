package keeper

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
)

// ValidatedBridgeExchange is the read-only result of ValidateBridgeExchange.
// DeliverTx mutation (AddBridgeTransactionValidator, SetBridgeTransaction,
// minting) is performed by the msg server using these fields.
type ValidatedBridgeExchange struct {
	ValidatorAddress sdk.AccAddress
	ProposedTx       *types.BridgeTransaction
	ExistingTx       *types.BridgeTransaction // nil if IsCreate
	ValidatorPower   int64
	TotalEpochPower  int64
	IsCreate         bool
	EpochIndex       uint64
}

// RequireActiveOrPreviousActiveParticipant returns nil if addr is in the
// ActiveParticipantsSet for the current epoch or the previous epoch.
// Matches MsgBridgeExchange permission OR of Active | PreviousActive.
func (k Keeper) RequireActiveOrPreviousActiveParticipant(ctx sdk.Context, addr sdk.AccAddress) error {
	currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		return err
	}
	found, err := k.ActiveParticipantsSet.Has(ctx, collections.Join(currentEpoch, addr))
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	if currentEpoch >= 1 {
		foundPrev, err := k.ActiveParticipantsSet.Has(ctx, collections.Join(currentEpoch-1, addr))
		if err != nil {
			return err
		}
		if foundPrev {
			return nil
		}
	}
	return types.ErrActiveParticipantNotFound
}

// RequireActiveParticipantAtOffset returns nil if addr is active in
// currentEpoch - epochOffset. Used by the permission framework.
func (k Keeper) RequireActiveParticipantAtOffset(ctx sdk.Context, addr sdk.AccAddress, epochOffset uint64) error {
	currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		return err
	}
	if currentEpoch < epochOffset {
		return types.ErrActiveParticipantNotFound
	}
	found, err := k.ActiveParticipantsSet.Has(ctx, collections.Join(currentEpoch-epochOffset, addr))
	if err != nil {
		return err
	}
	if !found {
		return types.ErrActiveParticipantNotFound
	}
	return nil
}

// ValidateBridgeExchange is read-only. No writes to bridge state, balances, or sequences.
func (k Keeper) ValidateBridgeExchange(ctx sdk.Context, msg *types.MsgBridgeExchange) (*ValidatedBridgeExchange, error) {
	addr, err := sdk.AccAddressFromBech32(msg.Validator)
	if err != nil {
		k.LogError(
			"Bridge exchange: failed to decode bech32 address",
			types.Messages,
			"error", err.Error())
		return nil, fmt.Errorf("invalid validator address: %v", err)
	}

	if err := k.RequireActiveOrPreviousActiveParticipant(ctx, addr); err != nil {
		return nil, err
	}

	_, ok := new(big.Int).SetString(msg.Amount, 10)
	if !ok {
		k.LogError("Bridge exchange: Invalid amount", types.Messages, "amount", msg.Amount)
		return nil, fmt.Errorf("invalid amount: %s", msg.Amount)
	}

	proposedTx := &types.BridgeTransaction{
		ChainId:         msg.OriginChain,
		ContractAddress: strings.ToLower(msg.ContractAddress),
		OwnerAddress:    msg.OwnerAddress,
		Amount:          msg.Amount,
		BlockNumber:     msg.BlockNumber,
		ReceiptIndex:    msg.ReceiptIndex,
		ReceiptsRoot:    msg.ReceiptsRoot,
	}

	existingTx, found := k.GetBridgeTransactionByContent(ctx, proposedTx)
	if found {
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
			return nil, types.ErrBridgeContentMismatch
		}

		epochGroup, err := k.GetEpochGroup(ctx, existingTx.EpochIndex, "")
		if err != nil {
			k.LogError("Bridge exchange: unable to get epoch group for existing transaction", types.Messages,
				"epochIndex", existingTx.EpochIndex, "error", err)
			return nil, fmt.Errorf("unable to get epoch group for existing transaction: %v", err)
		}

		epochGroupMembers, err := epochGroup.GetGroupMembers(ctx)
		if err != nil {
			k.LogError("Bridge exchange: unable to get epoch group members", types.Messages,
				"epochIndex", existingTx.EpochIndex, "error", err)
			return nil, fmt.Errorf("unable to get epoch group members: %v", err)
		}

		validatorPower, isInEpochGroup := memberPowerInGroup(addr, epochGroupMembers)
		if !isInEpochGroup {
			k.LogError("Bridge exchange: Validator not in transaction's epoch group", types.Messages,
				"validator", msg.Validator, "epochIndex", existingTx.EpochIndex)
			return nil, types.ErrBridgeValidatorNotInTxEpochGroup
		}

		alreadyValidated, err := k.HasBridgeTransactionValidator(ctx, existingTx, addr.String())
		if err != nil {
			k.LogError("Bridge exchange: Failed to check validator confirmation", types.Messages,
				"validator", msg.Validator, "error", err)
			return nil, fmt.Errorf("failed to check validator confirmation: %v", err)
		}
		if alreadyValidated {
			k.LogError("Bridge exchange: Validator has already validated this transaction", types.Messages, "validator", msg.Validator)
			return nil, types.ErrBridgeAlreadyValidated
		}

		return &ValidatedBridgeExchange{
			ValidatorAddress: addr,
			ProposedTx:       proposedTx,
			ExistingTx:       existingTx,
			ValidatorPower:   validatorPower,
			TotalEpochPower:  epochGroup.GroupData.TotalWeight,
			IsCreate:         false,
			EpochIndex:       existingTx.EpochIndex,
		}, nil
	}

	currentEpochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("Bridge exchange: unable to get current epoch group", types.Messages, "error", err)
		return nil, fmt.Errorf("unable to get current epoch group: %v", err)
	}

	currentEpochMembers, err := currentEpochGroup.GetGroupMembers(ctx)
	if err != nil {
		k.LogError("Bridge exchange: unable to get current epoch group members", types.Messages,
			"epochIndex", currentEpochGroup.GroupData.EpochIndex, "error", err)
		return nil, fmt.Errorf("unable to get current epoch group members: %v", err)
	}

	validatorPower, isActive := memberPowerInGroup(addr, currentEpochMembers)
	if !isActive {
		k.LogError("Bridge exchange: Validator not in active participants", types.Messages, "validator", msg.Validator)
		return nil, types.ErrBridgeValidatorNotInActiveGroup
	}

	return &ValidatedBridgeExchange{
		ValidatorAddress: addr,
		ProposedTx:       proposedTx,
		ExistingTx:       nil,
		ValidatorPower:   validatorPower,
		TotalEpochPower:  currentEpochGroup.GroupData.TotalWeight,
		IsCreate:         true,
		EpochIndex:       currentEpochGroup.GroupData.EpochIndex,
	}, nil
}

func memberPowerInGroup(addr sdk.AccAddress, members []*group.GroupMember) (int64, bool) {
	for _, member := range members {
		if member == nil || member.Member == nil {
			continue
		}
		memberAddr, err := sdk.AccAddressFromBech32(member.Member.Address)
		if err != nil {
			continue
		}
		if memberAddr.Equals(addr) {
			weight, err := strconv.ParseInt(member.Member.Weight, 10, 64)
			if err != nil {
				continue
			}
			return weight, true
		}
	}
	return 0, false
}
