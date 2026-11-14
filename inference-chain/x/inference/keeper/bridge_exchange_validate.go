package keeper

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/epochgroup"
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
	// VotedPower is the current weight of the members still in the group that
	// have voted, this vote included. Set only when !IsCreate.
	VotedPower    int64
	RequiredPower int64
	IsCreate      bool
	EpochIndex    uint64
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

		weights, err := k.bridgeGroupWeights(ctx, epochGroup, addr)
		if err != nil {
			k.LogError("Bridge exchange: unable to get epoch group members", types.Messages,
				"epochIndex", existingTx.EpochIndex, "error", err)
			return nil, fmt.Errorf("unable to get epoch group members: %v", err)
		}

		validatorPower, isInEpochGroup := weights.voterPower, weights.voterInGroup
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

		liveWeight := weights.liveWeight
		votedPower := validatorPower + existingTx.TotalValidationPower
		if liveWeight != epochGroup.GroupData.TotalWeight {
			// A member left the group: list the voters to drop its vote.
			k.hydrateBridgeTransactionValidators(ctx, existingTx)
			votedPower = validatorPower + liveVotedPower(existingTx.Validators, weights.members)
		}
		return &ValidatedBridgeExchange{
			ValidatorAddress: addr,
			ProposedTx:       proposedTx,
			ExistingTx:       existingTx,
			ValidatorPower:   validatorPower,
			TotalEpochPower:  liveWeight,
			VotedPower:       votedPower,
			RequiredPower:    bridgeRequiredPower(epochGroup.GroupData.TotalWeight, liveWeight),
			IsCreate:         false,
			EpochIndex:       existingTx.EpochIndex,
		}, nil
	}

	currentEpochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("Bridge exchange: unable to get current epoch group", types.Messages, "error", err)
		return nil, fmt.Errorf("unable to get current epoch group: %v", err)
	}

	currentWeights, err := k.bridgeGroupWeights(ctx, currentEpochGroup, addr)
	if err != nil {
		k.LogError("Bridge exchange: unable to get current epoch group members", types.Messages,
			"epochIndex", currentEpochGroup.GroupData.EpochIndex, "error", err)
		return nil, fmt.Errorf("unable to get current epoch group members: %v", err)
	}

	validatorPower, isActive := currentWeights.voterPower, currentWeights.voterInGroup
	if !isActive {
		k.LogError("Bridge exchange: Validator not in active participants", types.Messages, "validator", msg.Validator)
		return nil, types.ErrBridgeValidatorNotInActiveGroup
	}

	return &ValidatedBridgeExchange{
		ValidatorAddress: addr,
		ProposedTx:       proposedTx,
		ExistingTx:       nil,
		ValidatorPower:   validatorPower,
		TotalEpochPower:  currentWeights.liveWeight,
		IsCreate:         true,
		EpochIndex:       currentEpochGroup.GroupData.EpochIndex,
	}, nil
}

type bridgeWeights struct {
	voterPower   int64
	voterInGroup bool
	liveWeight   int64
	members      []*group.GroupMember // set whenever liveWeight differs from the epoch-start weight
}

// bridgeGroupWeights finds the voter's x/group weight and the live group weight.
// While nobody has left the group, x/group holds exactly the epoch-start
// ValidationWeights, so the voter is looked up there instead of listing every
// member; anything unusual falls back to the member list.
func (k Keeper) bridgeGroupWeights(ctx sdk.Context, eg *epochgroup.EpochGroup, addr sdk.AccAddress) (bridgeWeights, error) {
	liveWeight, err := eg.LiveTotalWeight(ctx)
	if err == nil && liveWeight == eg.GroupData.TotalWeight {
		if power, ok := k.epochStartWeight(ctx, eg.GroupData, addr); ok {
			return bridgeWeights{voterPower: power, voterInGroup: true, liveWeight: liveWeight}, nil
		}
	}
	members, err := eg.GetGroupMembers(ctx)
	if err != nil {
		return bridgeWeights{}, err
	}
	power, inGroup := memberPowerInGroup(addr, members)
	return bridgeWeights{voterPower: power, voterInGroup: inGroup, liveWeight: liveGroupWeight(members), members: members}, nil
}

// epochStartWeight returns ok=false unless addr has exactly one positive
// ValidationWeights entry and was not excluded in that epoch.
func (k Keeper) epochStartWeight(ctx sdk.Context, data *types.EpochGroupData, addr sdk.AccAddress) (int64, bool) {
	var power int64
	found := false
	for _, vw := range data.ValidationWeights {
		if vw == nil {
			return 0, false
		}
		vwAddr, err := sdk.AccAddressFromBech32(vw.MemberAddress)
		if err != nil {
			return 0, false
		}
		if !vwAddr.Equals(addr) {
			continue
		}
		if found || vw.Weight <= 0 {
			return 0, false
		}
		power, found = vw.Weight, true
	}
	if !found {
		return 0, false
	}
	excluded, err := k.ExcludedParticipantsMap.Has(ctx, collections.Join(data.EpochIndex, addr))
	if err != nil || excluded {
		return 0, false
	}
	return power, true
}

// liveGroupWeight sums the x/group weights of the members still in the group.
// GroupData.TotalWeight is fixed at epoch start; removed members no longer count.
func liveGroupWeight(members []*group.GroupMember) int64 {
	var total int64
	for _, member := range members {
		if member == nil || member.Member == nil {
			continue
		}
		weight, err := strconv.ParseInt(member.Member.Weight, 10, 64)
		if err != nil {
			continue
		}
		total += weight
	}
	return total
}

// bridgeRequiredPower is a majority of the weight still in the group, but never
// less than a third of the epoch-start weight, so a group that lost most of its
// members cannot complete records with a small remainder.
func bridgeRequiredPower(epochStartWeight, liveWeight int64) int64 {
	base := liveWeight
	if epochStartWeight > 0 && epochStartWeight < base {
		base = epochStartWeight
	}
	required := base/2 + 1
	if floor := epochStartWeight/3 + 1; floor > required {
		required = floor
	}
	return required
}

// liveVotedPower sums the current weights of the voters still in the group;
// votes of members removed after voting no longer count.
func liveVotedPower(voters []string, members []*group.GroupMember) int64 {
	weights := make(map[string]int64, len(members))
	for _, member := range members {
		if member == nil || member.Member == nil {
			continue
		}
		memberAddr, err := sdk.AccAddressFromBech32(member.Member.Address)
		if err != nil {
			continue
		}
		weight, err := strconv.ParseInt(member.Member.Weight, 10, 64)
		if err != nil {
			continue
		}
		weights[memberAddr.String()] = weight
	}
	var total int64
	for _, voter := range voters {
		voterAddr, err := sdk.AccAddressFromBech32(voter)
		if err != nil {
			continue
		}
		total += weights[voterAddr.String()]
	}
	return total
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
