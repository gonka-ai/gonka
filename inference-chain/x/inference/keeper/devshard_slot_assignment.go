package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) devshardSlotWeights(ctx context.Context, epochIndex uint64, modelID string) ([]calculations.WeightEntry, int64, error) {
	epochGroup, err := k.GetEpochGroup(ctx, epochIndex, modelID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get epoch group for model %q: %w", modelID, err)
	}
	if epochGroup.GroupData == nil || len(epochGroup.GroupData.ValidationWeights) == 0 {
		return nil, 0, fmt.Errorf("no validation weights in epoch group for model %q", modelID)
	}

	weights := make(map[string]int64)
	for _, vw := range epochGroup.GroupData.ValidationWeights {
		if k.IsUnderChallenge(ctx, vw.MemberAddress) {
			continue
		}
		weights[vw.MemberAddress] = vw.Weight
	}
	sortedEntries, totalWeight := calculations.PrepareSortedEntries(weights)
	if totalWeight <= 0 {
		return nil, 0, fmt.Errorf("total weight is zero")
	}
	return sortedEntries, totalWeight, nil
}

func (k Keeper) IsDevshardEscrowPendingSlots(ctx context.Context, id uint64) bool {
	pending, err := k.DevshardEscrowsPendingSlots.Has(ctx, id)
	return err == nil && pending
}

func (k Keeper) AssignPendingDevshardSlots(ctx context.Context) error {
	iter, err := k.DevshardEscrowsPendingSlots.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	pending, err := iter.KeyValues()
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	appHash := hex.EncodeToString(sdkCtx.HeaderInfo().AppHash)
	for _, kv := range pending {
		cacheCtx, write := sdkCtx.CacheContext()
		if err := k.assignDevshardSlots(cacheCtx, kv.Key, appHash, int(kv.Value)); err != nil {
			k.LogError("failed to assign devshard escrow slots", types.Allocation, "escrow_id", kv.Key, "error", err)
			continue
		}
		write()
	}
	return nil
}

func (k Keeper) assignDevshardSlots(ctx context.Context, id uint64, appHash string, groupSize int) error {
	escrow, err := k.DevshardEscrows.Get(ctx, id)
	if errors.Is(err, collections.ErrNotFound) {
		return k.DevshardEscrowsPendingSlots.Remove(ctx, id)
	}
	if err != nil {
		return err
	}
	if escrow.Settled || len(escrow.Slots) > 0 {
		return k.DevshardEscrowsPendingSlots.Remove(ctx, id)
	}

	sortedEntries, totalWeight, err := k.devshardSlotWeights(ctx, escrow.EpochIndex, escrow.ModelId)
	if err != nil {
		return k.refundUnassignedDevshardEscrow(ctx, escrow, err)
	}

	escrow.Slots = calculations.GetSlotsFromSorted(
		appHash,
		fmt.Sprintf("devshard_escrow:%d", escrow.Id),
		escrow.ModelId,
		sortedEntries,
		totalWeight,
		groupSize,
	)
	escrow.AppHash = appHash
	if err := k.SetDevshardEscrow(ctx, escrow); err != nil {
		return err
	}
	if err := k.DevshardEscrowsPendingSlots.Remove(ctx, id); err != nil {
		return err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"devshard_escrow_slots_assigned",
		sdk.NewAttribute("escrow_id", fmt.Sprint(escrow.Id)),
		sdk.NewAttribute("epoch_index", fmt.Sprint(escrow.EpochIndex)),
		sdk.NewAttribute("model_id", escrow.ModelId),
		sdk.NewAttribute("app_hash", appHash),
	))
	return nil
}

func (k Keeper) refundUnassignedDevshardEscrow(ctx context.Context, escrow types.DevshardEscrow, cause error) error {
	creatorAddr, err := sdk.AccAddressFromBech32(escrow.Creator)
	if err != nil {
		return fmt.Errorf("invalid creator address: %w", err)
	}
	coins, err := types.GetCoins(int64(escrow.Amount))
	if err != nil {
		return fmt.Errorf("invalid amount: %w", err)
	}
	if err := k.BankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, creatorAddr, coins, "devshard_escrow_unassigned_refund"); err != nil {
		return fmt.Errorf("failed to refund escrow: %w", err)
	}

	escrow.Settled = true
	if err := k.SetDevshardEscrow(ctx, escrow); err != nil {
		return err
	}
	if err := k.DevshardEscrowsPendingSlots.Remove(ctx, escrow.Id); err != nil {
		return err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"devshard_escrow_slots_unassigned",
		sdk.NewAttribute("escrow_id", fmt.Sprint(escrow.Id)),
		sdk.NewAttribute("creator", escrow.Creator),
		sdk.NewAttribute("amount", fmt.Sprint(escrow.Amount)),
		sdk.NewAttribute("reason", cause.Error()),
	))
	return nil
}
