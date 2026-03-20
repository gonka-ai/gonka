package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SettleSubnetEscrow(goCtx context.Context, msg *types.MsgSettleSubnetEscrow) (*types.MsgSettleSubnetEscrowResponse, error) {
	if err := k.CheckPermission(goCtx, msg, EscrowAllowListPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	escrow, found := k.GetSubnetEscrow(goCtx, msg.EscrowId)
	if !found {
		return nil, fmt.Errorf("escrow %d not found", msg.EscrowId)
	}

	warmKeyChecker := func(granter, grantee string) bool {
		return k.HasWarmKeyGrant(goCtx, granter, grantee)
	}
	if err := VerifySubnetSettlement(escrow, msg, warmKeyChecker); err != nil {
		return nil, err
	}

	// Aggregate costs per unique validator address (deterministic: iterate by slot order)
	validatorCosts := make(map[string]uint64)
	for _, hs := range msg.HostStats {
		if int(hs.SlotId) >= len(escrow.Slots) {
			return nil, fmt.Errorf("host_stats slot_id %d out of range", hs.SlotId)
		}
		addr := escrow.Slots[hs.SlotId]
		validatorCosts[addr] += hs.Cost
	}

	// Pay validators in slot order (deterministic iteration over escrow.Slots)
	var totalCost uint64
	paidValidators := make(map[string]bool)
	for _, addr := range escrow.Slots {
		cost, hasCost := validatorCosts[addr]
		if !hasCost || cost == 0 || paidValidators[addr] {
			continue
		}
		paidValidators[addr] = true
		totalCost += cost

		recipientAddr, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid validator address %s: %w", addr, err)
		}
		coins, err := types.GetCoins(int64(cost))
		if err != nil {
			return nil, fmt.Errorf("invalid cost amount: %w", err)
		}
		err = k.BankKeeper.SendCoinsFromModuleToAccount(goCtx, types.ModuleName, recipientAddr, coins, "subnet_escrow_payment")
		if err != nil {
			return nil, fmt.Errorf("failed to pay validator %s: %w", addr, err)
		}
	}

	// Refund remainder to creator
	remainder := escrow.Amount - totalCost
	if remainder > 0 {
		creatorAddr, err := sdk.AccAddressFromBech32(escrow.Creator)
		if err != nil {
			return nil, fmt.Errorf("invalid creator address: %w", err)
		}
		coins, err := types.GetCoins(int64(remainder))
		if err != nil {
			return nil, fmt.Errorf("invalid refund amount: %w", err)
		}
		err = k.BankKeeper.SendCoinsFromModuleToAccount(goCtx, types.ModuleName, creatorAddr, coins, "subnet_escrow_refund")
		if err != nil {
			return nil, fmt.Errorf("failed to refund creator: %w", err)
		}
	}

	// Aggregate host stats per validator per epoch (deterministic: iterate msg.HostStats by slot_id order)
	seenValidators := make(map[string]bool)
	for _, hs := range msg.HostStats {
		addr := escrow.Slots[hs.SlotId]
		participantAddr, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid participant address %s: %w", addr, err)
		}
		if err := k.AggregateSubnetHostStats(goCtx, escrow.EpochIndex, participantAddr, *hs); err != nil {
			return nil, fmt.Errorf("failed to aggregate host stats: %w", err)
		}
		if !seenValidators[addr] {
			seenValidators[addr] = true
			if err := k.IncrementSubnetHostEscrowCount(goCtx, escrow.EpochIndex, participantAddr); err != nil {
				return nil, fmt.Errorf("failed to increment escrow count: %w", err)
			}
		}
	}

	escrow.Settled = true
	if err := k.SetSubnetEscrow(goCtx, escrow); err != nil {
		return nil, fmt.Errorf("failed to update escrow: %w", err)
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"subnet_escrow_settled",
		sdk.NewAttribute("escrow_id", fmt.Sprint(escrow.Id)),
		sdk.NewAttribute("settler", msg.Settler),
		sdk.NewAttribute("total_cost", fmt.Sprint(totalCost)),
		sdk.NewAttribute("remainder", fmt.Sprint(remainder)),
	))

	return &types.MsgSettleSubnetEscrowResponse{}, nil
}
