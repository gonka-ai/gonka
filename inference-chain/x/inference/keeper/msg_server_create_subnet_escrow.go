package keeper

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CreateSubnetEscrow(goCtx context.Context, msg *types.MsgCreateSubnetEscrow) (*types.MsgCreateSubnetEscrowResponse, error) {
	if err := k.CheckPermission(goCtx, msg, EscrowAllowListPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	ep := k.GetSubnetEscrowParams(goCtx)

	if msg.Amount < ep.MinAmount || msg.Amount > ep.MaxAmount {
		return nil, fmt.Errorf("escrow amount %d out of range [%d, %d]", msg.Amount, ep.MinAmount, ep.MaxAmount)
	}

	epochIndex, ok := k.GetEffectiveEpochIndex(goCtx)
	if !ok {
		return nil, fmt.Errorf("failed to get effective epoch index")
	}

	epochCount := k.GetSubnetEscrowEpochCount(goCtx, epochIndex)
	if epochCount >= uint64(ep.MaxEscrowsPerEpoch) {
		return nil, fmt.Errorf("epoch %d already has %d escrows (max %d)", epochIndex, epochCount, ep.MaxEscrowsPerEpoch)
	}

	epochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current epoch group: %w", err)
	}
	if epochGroup.GroupData == nil || len(epochGroup.GroupData.ValidationWeights) == 0 {
		return nil, fmt.Errorf("no validation weights in current epoch group")
	}

	weights := make(map[string]int64)
	for _, vw := range epochGroup.GroupData.ValidationWeights {
		weights[vw.MemberAddress] = vw.Weight
	}
	sortedEntries, totalWeight := calculations.PrepareSortedEntries(weights)
	if totalWeight <= 0 {
		return nil, fmt.Errorf("total weight is zero")
	}

	appHash := hex.EncodeToString(ctx.HeaderInfo().AppHash)

	// We need the escrow ID for slot sampling, but we don't have it yet.
	// Reserve the next counter value first.
	counter, err := k.SubnetEscrowCounter.Get(goCtx)
	if err != nil {
		counter = 0
	}
	if counter == math.MaxUint64 {
		return nil, fmt.Errorf("subnet escrow counter overflow")
	}
	nextID := counter + 1

	slots := calculations.GetSlotsFromSorted(appHash, fmt.Sprintf("subnet_escrow:%d", nextID), sortedEntries, totalWeight, int(ep.GroupSize))

	creatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, fmt.Errorf("invalid creator address: %w", err)
	}

	coins, err := types.GetCoins(int64(msg.Amount))
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}
	err = k.BankKeeper.SendCoinsFromAccountToModule(goCtx, creatorAddr, types.ModuleName, coins, "subnet_escrow_lock")
	if err != nil {
		return nil, fmt.Errorf("failed to lock funds: %w", err)
	}

	escrow := &types.SubnetEscrow{
		Creator:    msg.Creator,
		Amount:     msg.Amount,
		Slots:      slots,
		EpochIndex: epochIndex,
		AppHash:    appHash,
		Settled:    false,
		TokenPrice: ep.TokenPrice,
	}

	id, err := k.StoreSubnetEscrow(goCtx, escrow, nextID)
	if err != nil {
		return nil, fmt.Errorf("failed to create escrow: %w", err)
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"subnet_escrow_created",
		sdk.NewAttribute("escrow_id", fmt.Sprint(id)),
		sdk.NewAttribute("creator", msg.Creator),
		sdk.NewAttribute("amount", fmt.Sprint(msg.Amount)),
		sdk.NewAttribute("epoch_index", fmt.Sprint(epochIndex)),
	))

	return &types.MsgCreateSubnetEscrowResponse{EscrowId: id}, nil
}
