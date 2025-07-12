package keeper

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/collateral/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

type (
	Keeper struct {
		cdc          codec.BinaryCodec
		storeService store.KVStoreService
		logger       log.Logger

		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority string

		bankKeeper       types.BankKeeper
		bankEscrowKeeper types.BankEscrowKeeper
		stakingKeeper    types.StakingKeeper
	}
)

func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	logger log.Logger,
	authority string,

	bankKeeper types.BankKeeper,
	bankEscrowKeeper types.BankEscrowKeeper,
	stakingKeeper types.StakingKeeper,
) Keeper {
	if _, err := sdk.AccAddressFromBech32(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address: %s", authority))
	}

	return Keeper{
		cdc:          cdc,
		storeService: storeService,
		authority:    authority,
		logger:       logger,

		bankKeeper:       bankKeeper,
		bankEscrowKeeper: bankEscrowKeeper,
		stakingKeeper:    stakingKeeper,
	}
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() string {
	return k.authority
}

// Logger returns a module-specific logger.
func (k Keeper) Logger() log.Logger {
	return k.logger.With("module", fmt.Sprintf("x/%s", types.ModuleName))
}

// SetCollateral stores a participant's collateral amount
func (k Keeper) SetCollateral(ctx sdk.Context, participantAddress string, amount sdk.Coin) {
	store := k.storeService.OpenKVStore(ctx)
	bz := k.cdc.MustMarshal(&amount)
	err := store.Set(types.GetCollateralKey(participantAddress), bz)
	if err != nil {
		panic(err)
	}
}

// GetCollateral retrieves a participant's collateral amount
func (k Keeper) GetCollateral(ctx sdk.Context, participantAddress string) (sdk.Coin, bool) {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(types.GetCollateralKey(participantAddress))
	if err != nil {
		panic(err)
	}
	if bz == nil {
		return sdk.Coin{}, false
	}

	var amount sdk.Coin
	k.cdc.MustUnmarshal(bz, &amount)
	return amount, true
}

// RemoveCollateral removes a participant's collateral from the store
func (k Keeper) RemoveCollateral(ctx sdk.Context, participantAddress string) {
	store := k.storeService.OpenKVStore(ctx)
	err := store.Delete(types.GetCollateralKey(participantAddress))
	if err != nil {
		panic(err)
	}
}

// GetAllCollateral returns all collateral entries
func (k Keeper) GetAllCollateral(ctx sdk.Context) map[string]sdk.Coin {
	store := k.storeService.OpenKVStore(ctx)
	collateralMap := make(map[string]sdk.Coin)

	iterator, err := store.Iterator(types.CollateralKey, nil)
	if err != nil {
		panic(err)
	}
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		// Extract participant address from the key
		key := iterator.Key()
		participantAddr := string(key[len(types.CollateralKey):])

		// Unmarshal the collateral amount
		var amount sdk.Coin
		k.cdc.MustUnmarshal(iterator.Value(), &amount)

		collateralMap[participantAddr] = amount
	}

	return collateralMap
}

// SetUnbondingCollateral stores an unbonding entry, adding to existing if already present
func (k Keeper) SetUnbondingCollateral(ctx sdk.Context, unbonding types.UnbondingCollateral) {
	store := k.storeService.OpenKVStore(ctx)

	// Check if an entry already exists for this epoch and participant
	existing, found := k.GetUnbondingCollateral(ctx, unbonding.Participant, unbonding.CompletionEpoch)
	if found {
		// Add to the existing amount
		unbonding.Amount = unbonding.Amount.Add(existing.Amount)
	}

	bz := k.cdc.MustMarshal(&unbonding)
	key := types.GetUnbondingKey(unbonding.CompletionEpoch, unbonding.Participant)
	err := store.Set(key, bz)
	if err != nil {
		panic(err)
	}
}

// GetUnbondingCollateral retrieves a specific unbonding entry
func (k Keeper) GetUnbondingCollateral(ctx sdk.Context, participantAddress string, completionEpoch uint64) (types.UnbondingCollateral, bool) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GetUnbondingKey(completionEpoch, participantAddress)
	bz, err := store.Get(key)
	if err != nil {
		panic(err)
	}
	if bz == nil {
		return types.UnbondingCollateral{}, false
	}

	var unbonding types.UnbondingCollateral
	k.cdc.MustUnmarshal(bz, &unbonding)
	return unbonding, true
}

// RemoveUnbondingCollateral removes an unbonding entry
func (k Keeper) RemoveUnbondingCollateral(ctx sdk.Context, participantAddress string, completionEpoch uint64) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GetUnbondingKey(completionEpoch, participantAddress)
	err := store.Delete(key)
	if err != nil {
		panic(err)
	}
}

// RemoveUnbondingByEpoch removes all unbonding entries for a specific epoch
// This is useful for batch processing at the end of an epoch
func (k Keeper) RemoveUnbondingByEpoch(ctx sdk.Context, completionEpoch uint64) {
	store := k.storeService.OpenKVStore(ctx)

	prefix := types.GetUnbondingEpochPrefix(completionEpoch)
	iterator, err := store.Iterator(prefix, nil)
	if err != nil {
		panic(err)
	}
	defer iterator.Close()

	// Collect keys to delete (can't delete while iterating)
	keysToDelete := [][]byte{}
	for ; iterator.Valid(); iterator.Next() {
		keysToDelete = append(keysToDelete, iterator.Key())
	}

	// Delete all collected keys
	for _, key := range keysToDelete {
		err := store.Delete(key)
		if err != nil {
			panic(err)
		}
	}
}

// GetUnbondingByEpoch returns all unbonding entries for a specific epoch
func (k Keeper) GetUnbondingByEpoch(ctx sdk.Context, completionEpoch uint64) []types.UnbondingCollateral {
	store := k.storeService.OpenKVStore(ctx)
	unbondingList := []types.UnbondingCollateral{}

	prefix := types.GetUnbondingEpochPrefix(completionEpoch)
	iterator, err := store.Iterator(prefix, nil)
	if err != nil {
		panic(err)
	}
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var unbonding types.UnbondingCollateral
		k.cdc.MustUnmarshal(iterator.Value(), &unbonding)
		unbondingList = append(unbondingList, unbonding)
	}

	return unbondingList
}

// GetUnbondingByParticipant returns all unbonding entries for a specific participant
func (k Keeper) GetUnbondingByParticipant(ctx sdk.Context, participantAddress string) []types.UnbondingCollateral {
	store := k.storeService.OpenKVStore(ctx)
	unbondingList := []types.UnbondingCollateral{}

	// We need to iterate through all unbonding entries and filter by participant
	iterator, err := store.Iterator(types.UnbondingKey, nil)
	if err != nil {
		panic(err)
	}
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var unbonding types.UnbondingCollateral
		k.cdc.MustUnmarshal(iterator.Value(), &unbonding)

		if unbonding.Participant == participantAddress {
			unbondingList = append(unbondingList, unbonding)
		}
	}

	return unbondingList
}

// GetCurrentEpoch retrieves the current epoch from the store.
func (k Keeper) GetCurrentEpoch(ctx sdk.Context) uint64 {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(types.CurrentEpochKey)
	if err != nil {
		panic(err)
	}
	if bz == nil {
		return 0 // Default to epoch 0 if not set
	}
	return binary.BigEndian.Uint64(bz)
}

// SetCurrentEpoch sets the current epoch in the store.
func (k Keeper) SetCurrentEpoch(ctx sdk.Context, epoch uint64) {
	store := k.storeService.OpenKVStore(ctx)
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, epoch)
	err := store.Set(types.CurrentEpochKey, bz)
	if err != nil {
		panic(err)
	}
}

// AdvanceEpoch is called by an external module (inference) to signal an epoch transition.
// It processes the unbonding queue for the completed epoch and increments the internal epoch counter.
func (k Keeper) AdvanceEpoch(ctx sdk.Context, completedEpoch uint64) {
	k.Logger().Info("advancing epoch in collateral module", "completed_epoch", completedEpoch)

	// Process unbonding queue for the epoch that just finished
	k.ProcessUnbondingQueue(ctx, completedEpoch)

	// Increment the epoch counter
	nextEpoch := completedEpoch + 1
	k.SetCurrentEpoch(ctx, nextEpoch)
}

// ProcessUnbondingQueue iterates through all unbonding entries for a given epoch,
// releases the funds back to the participants, and removes the processed entries.
func (k Keeper) ProcessUnbondingQueue(ctx sdk.Context, completionEpoch uint64) {
	unbondingEntries := k.GetUnbondingByEpoch(ctx, completionEpoch)

	for _, entry := range unbondingEntries {
		participantAddr, err := sdk.AccAddressFromBech32(entry.Participant)
		if err != nil {
			// This should ideally not happen if addresses are validated on entry
			k.Logger().Error("failed to parse participant address during unbonding processing",
				"participant", entry.Participant, "error", err)
			continue // Skip this entry
		}

		// Send funds from the module account back to the participant
		err = k.bankEscrowKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, participantAddr, sdk.NewCoins(entry.Amount))
		if err != nil {
			// This is a critical error, as it implies the module account is underfunded
			// which should not happen if logic is correct.
			panic(fmt.Sprintf("failed to release unbonding collateral for %s: %v", entry.Participant, err))
		}

		// Emit event for successful withdrawal processing
		ctx.EventManager().EmitEvents(sdk.Events{
			sdk.NewEvent(
				types.EventTypeProcessWithdrawal,
				sdk.NewAttribute(types.AttributeKeyParticipant, entry.Participant),
				sdk.NewAttribute(types.AttributeKeyAmount, entry.Amount.String()),
				sdk.NewAttribute(types.AttributeKeyCompletionEpoch, strconv.FormatUint(completionEpoch, 10)),
			),
		})

		k.Logger().Info("processed collateral withdrawal",
			"participant", entry.Participant,
			"amount", entry.Amount.String(),
			"completion_epoch", completionEpoch,
		)
	}

	// Remove all processed entries for this epoch
	if len(unbondingEntries) > 0 {
		k.RemoveUnbondingByEpoch(ctx, completionEpoch)
	}
}

// GetAllUnbonding returns all unbonding entries (for genesis export)
func (k Keeper) GetAllUnbonding(ctx sdk.Context) []types.UnbondingCollateral {
	store := k.storeService.OpenKVStore(ctx)
	unbondingList := []types.UnbondingCollateral{}

	iterator, err := store.Iterator(types.UnbondingKey, nil)
	if err != nil {
		panic(err)
	}
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var unbonding types.UnbondingCollateral
		k.cdc.MustUnmarshal(iterator.Value(), &unbonding)
		unbondingList = append(unbondingList, unbonding)
	}

	return unbondingList
}

// Slash penalizes a participant by burning a fraction of their total collateral.
// This includes both their active collateral and any collateral in the unbonding queue.
// The slash is applied proportionally to all holdings.
func (k Keeper) Slash(ctx sdk.Context, participantAddress string, slashFraction math.LegacyDec) (sdk.Coin, error) {
	if slashFraction.IsNegative() || slashFraction.GT(math.LegacyOneDec()) {
		return sdk.Coin{}, fmt.Errorf("slash fraction must be between 0 and 1, got %s", slashFraction)
	}

	totalSlashedAmount := sdk.NewCoin(inferencetypes.BaseCoin, math.ZeroInt())

	// 1. Slash active collateral
	activeCollateral, found := k.GetCollateral(ctx, participantAddress)
	if found {
		slashAmountDec := math.LegacyNewDecFromInt(activeCollateral.Amount).Mul(slashFraction)
		slashAmount := sdk.NewCoin(activeCollateral.Denom, slashAmountDec.TruncateInt())

		if !slashAmount.IsZero() {
			newCollateral := activeCollateral.Sub(slashAmount)
			k.SetCollateral(ctx, participantAddress, newCollateral)
			totalSlashedAmount = totalSlashedAmount.Add(slashAmount)
		}
	}

	// 2. Slash unbonding collateral
	unbondingEntries := k.GetUnbondingByParticipant(ctx, participantAddress)
	for _, entry := range unbondingEntries {
		slashAmountDec := math.LegacyNewDecFromInt(entry.Amount.Amount).Mul(slashFraction)
		slashAmount := sdk.NewCoin(entry.Amount.Denom, slashAmountDec.TruncateInt())

		if !slashAmount.IsZero() {
			newUnbondingAmount := entry.Amount.Sub(slashAmount)
			entry.Amount = newUnbondingAmount

			// If the unbonding entry is now zero, remove it. Otherwise, update it.
			if newUnbondingAmount.IsZero() {
				k.RemoveUnbondingCollateral(ctx, entry.Participant, entry.CompletionEpoch)
			} else {
				k.SetUnbondingCollateral(ctx, entry)
			}
			totalSlashedAmount = totalSlashedAmount.Add(slashAmount)
		}
	}

	// 3. Burn the total slashed amount from the module account
	if !totalSlashedAmount.IsZero() {
		err := k.bankEscrowKeeper.BurnCoins(ctx, types.ModuleName, sdk.NewCoins(totalSlashedAmount))
		if err != nil {
			// This is a critical error, indicating an issue with the module account or supply
			return sdk.Coin{}, fmt.Errorf("failed to burn slashed coins: %w", err)
		}

		// 4. Emit a slash event
		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				types.EventTypeSlashCollateral,
				sdk.NewAttribute(types.AttributeKeyParticipant, participantAddress),
				sdk.NewAttribute(types.AttributeKeySlashAmount, totalSlashedAmount.String()),
				sdk.NewAttribute(types.AttributeKeySlashFraction, slashFraction.String()),
			),
		)

		k.Logger().Info("slashed participant collateral",
			"participant", participantAddress,
			"slash_fraction", slashFraction.String(),
			"slashed_amount", totalSlashedAmount.String(),
		)
	}

	return totalSlashedAmount, nil
}
