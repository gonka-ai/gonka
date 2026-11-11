package keeper

import (
	"fmt"
	"math/big"
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
	IsRetry          bool
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

		if !isGroupMember(addr, epochGroupMembers) {
			k.LogError("Bridge exchange: Validator not in transaction's epoch group", types.Messages,
				"validator", msg.Validator, "epochIndex", existingTx.EpochIndex)
			return nil, types.ErrBridgeValidatorNotInTxEpochGroup
		}

		trust, validatorPower, err := k.bridgeTrustPower(ctx, existingTx.EpochIndex, addr)
		if err != nil {
			return nil, err
		}

		alreadyValidated, err := k.HasBridgeTransactionValidator(ctx, existingTx, addr.String())
		if err != nil {
			k.LogError("Bridge exchange: Failed to check validator confirmation", types.Messages,
				"validator", msg.Validator, "error", err)
			return nil, fmt.Errorf("failed to check validator confirmation: %v", err)
		}
		isRetry := false
		if alreadyValidated {
			if !bridgeMajorityPending(existingTx, trust.Total) {
				k.LogError("Bridge exchange: Validator has already validated this transaction", types.Messages, "validator", msg.Validator)
				return nil, types.ErrBridgeAlreadyValidated
			}
			isRetry = true
			validatorPower = 0
		}

		return &ValidatedBridgeExchange{
			ValidatorAddress: addr,
			ProposedTx:       proposedTx,
			ExistingTx:       existingTx,
			ValidatorPower:   validatorPower,
			TotalEpochPower:  trust.Total,
			IsCreate:         false,
			IsRetry:          isRetry,
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

	if !isGroupMember(addr, currentEpochMembers) {
		k.LogError("Bridge exchange: Validator not in active participants", types.Messages, "validator", msg.Validator)
		return nil, types.ErrBridgeValidatorNotInActiveGroup
	}

	trust, validatorPower, err := k.bridgeTrustPower(ctx, currentEpochGroup.GroupData.EpochIndex, addr)
	if err != nil {
		return nil, err
	}

	return &ValidatedBridgeExchange{
		ValidatorAddress: addr,
		ProposedTx:       proposedTx,
		ExistingTx:       nil,
		ValidatorPower:   validatorPower,
		TotalEpochPower:  trust.Total,
		IsCreate:         true,
		EpochIndex:       currentEpochGroup.GroupData.EpochIndex,
	}, nil
}

func bridgeMajorityThreshold(totalEpochPower int64) int64 {
	return totalEpochPower/2 + 1
}

func bridgeMajorityPending(tx *types.BridgeTransaction, totalEpochPower int64) bool {
	return tx.Status == types.BridgeTransactionStatus_BRIDGE_PENDING &&
		totalEpochPower > 0 &&
		tx.TotalValidationPower >= bridgeMajorityThreshold(totalEpochPower)
}

func (k Keeper) bridgeTrustPower(ctx sdk.Context, epochIndex uint64, addr sdk.AccAddress) (EpochTrust, int64, error) {
	trust, err := k.GetEpochTrust(ctx, epochIndex)
	if err != nil {
		k.LogError("Bridge exchange: unable to load trust weights", types.Messages,
			"epochIndex", epochIndex, "error", err)
		return EpochTrust{}, 0, err
	}
	power, err := k.EpochTrustWeight(ctx, trust, addr)
	if err != nil {
		k.LogError("Bridge exchange: unable to load validator trust weight", types.Messages,
			"epochIndex", epochIndex, "validator", addr.String(), "error", err)
		return EpochTrust{}, 0, err
	}
	return trust, power, nil
}

func isGroupMember(addr sdk.AccAddress, members []*group.GroupMember) bool {
	for _, member := range members {
		if member == nil || member.Member == nil {
			continue
		}
		memberAddr, err := sdk.AccAddressFromBech32(member.Member.Address)
		if err != nil {
			continue
		}
		if memberAddr.Equals(addr) {
			return true
		}
	}
	return false
}
