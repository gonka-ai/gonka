package keeper

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
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

		bankViewKeeper        types.BankKeeper
		bookkeepingBankKeeper types.BookkeepingBankKeeper
	}
)

func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	logger log.Logger,
	authority string,

	bankKeeper types.BankKeeper,
	bookkeepingBankKeeper types.BookkeepingBankKeeper,
) Keeper {
	if _, err := sdk.AccAddressFromBech32(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address: %s", authority))
	}

	return Keeper{
		cdc:          cdc,
		storeService: storeService,
		authority:    authority,
		logger:       logger,

		bankViewKeeper:        bankKeeper,
		bookkeepingBankKeeper: bookkeepingBankKeeper,
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
func (k Keeper) SetCollateral(ctx context.Context, participantAddress sdk.AccAddress, amount sdk.Coin) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	store := k.storeService.OpenKVStore(sdkCtx)
	bz, err := k.cdc.Marshal(&amount)
	if err != nil {
		panic(err)
	}
	err = store.Set(types.GetCollateralKey(participantAddress.String()), bz)
	if err != nil {
		panic(err)
	}
}

// GetCollateral retrieves a participant's collateral amount
func (k Keeper) GetCollateral(ctx context.Context, participantAddress sdk.AccAddress) (sdk.Coin, bool) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	store := k.storeService.OpenKVStore(sdkCtx)
	bz, err := store.Get(types.GetCollateralKey(participantAddress.String()))
	if err != nil {
		panic(err)
	}
	if bz == nil {
		return sdk.Coin{}, false
	}

	var amount sdk.Coin
	err = k.cdc.Unmarshal(bz, &amount)
	if err != nil {
		panic(err)
	}
	return amount, true
}

// RemoveCollateral removes a participant's collateral from the store
func (k Keeper) RemoveCollateral(ctx context.Context, participantAddress sdk.AccAddress) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	store := k.storeService.OpenKVStore(sdkCtx)
	err := store.Delete(types.GetCollateralKey(participantAddress.String()))
	if err != nil {
		panic(err)
	}
}

// GetAllCollaterals returns all collateral entries
func (k Keeper) GetAllCollaterals(ctx context.Context) map[string]sdk.Coin {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(sdkCtx))
	collateralStore := prefix.NewStore(storeAdapter, types.CollateralKey)
	collateralMap := make(map[string]sdk.Coin)

	iterator := collateralStore.Iterator(nil, nil)

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		// Extract participant address from the key
		participantAddr := string(iterator.Key())

		// Unmarshal the collateral amount
		var amount sdk.Coin
		err := k.cdc.Unmarshal(iterator.Value(), &amount)
		if err != nil {
			panic(err)
		}

		collateralMap[participantAddr] = amount
	}

	return collateralMap
}

// AddUnbondingCollateral stores an unbonding entry, adding to the amount if one already exists for the same participant and epoch.
func (k Keeper) AddUnbondingCollateral(ctx sdk.Context, participantAddress sdk.AccAddress, completionEpoch uint64, amount sdk.Coin) {
	// Check if an entry already exists for this epoch and participant
	existing, found := k.GetUnbondingCollateral(ctx, participantAddress, completionEpoch)
	if found {
		// Add to the existing amount
		amount = amount.Add(existing.Amount)
	}

	unbonding := types.UnbondingCollateral{
		Participant:     participantAddress.String(),
		CompletionEpoch: completionEpoch,
		Amount:          amount,
	}

	k.setUnbondingCollateralEntry(ctx, unbonding)
}

// setUnbondingCollateralEntry writes an unbonding entry directly to the store, overwriting any existing entry.
// This is an internal helper to be used by functions like Slash that need to update state without aggregation.
func (k Keeper) setUnbondingCollateralEntry(ctx sdk.Context, unbonding types.UnbondingCollateral) {
	store := k.storeService.OpenKVStore(ctx)
	bz := k.cdc.MustMarshal(&unbonding)
	key := types.GetUnbondingKey(unbonding.CompletionEpoch, unbonding.Participant)
	err := store.Set(key, bz)
	if err != nil {
		panic(err)
	}
}

// GetUnbondingCollateral retrieves a specific unbonding entry
func (k Keeper) GetUnbondingCollateral(ctx sdk.Context, participantAddress sdk.AccAddress, completionEpoch uint64) (types.UnbondingCollateral, bool) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GetUnbondingKey(completionEpoch, participantAddress.String())
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
func (k Keeper) RemoveUnbondingCollateral(ctx sdk.Context, participantAddress sdk.AccAddress, completionEpoch uint64) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GetUnbondingKey(completionEpoch, participantAddress.String())
	err := store.Delete(key)
	if err != nil {
		panic(err)
	}
}

// RemoveUnbondingByEpoch removes all unbonding entries for a specific epoch
// This is useful for batch processing at the end of an epoch
func (k Keeper) RemoveUnbondingByEpoch(ctx sdk.Context, completionEpoch uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	unbondingStore := prefix.NewStore(storeAdapter, types.GetUnbondingEpochPrefix(completionEpoch))

	iterator := unbondingStore.Iterator(nil, nil)
	defer iterator.Close()

	// Collect keys to delete (can't delete while iterating)
	keysToDelete := [][]byte{}
	for ; iterator.Valid(); iterator.Next() {
		keysToDelete = append(keysToDelete, iterator.Key())
	}

	// Delete all collected keys
	for _, key := range keysToDelete {
		unbondingStore.Delete(key)
	}
}

// GetUnbondingByEpoch returns all unbonding entries for a specific epoch
func (k Keeper) GetUnbondingByEpoch(ctx sdk.Context, completionEpoch uint64) []types.UnbondingCollateral {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	unbondingStore := prefix.NewStore(storeAdapter, types.GetUnbondingEpochPrefix(completionEpoch))
	unbondingList := []types.UnbondingCollateral{}

	iterator := unbondingStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var unbonding types.UnbondingCollateral
		k.cdc.MustUnmarshal(iterator.Value(), &unbonding)
		unbondingList = append(unbondingList, unbonding)
	}

	return unbondingList
}

// GetUnbondingByParticipant returns all unbonding entries for a specific participant
func (k Keeper) GetUnbondingByParticipant(ctx sdk.Context, participantAddress sdk.AccAddress) []types.UnbondingCollateral {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	unbondingStore := prefix.NewStore(storeAdapter, types.UnbondingKey)
	unbondingList := []types.UnbondingCollateral{}

	// We need to iterate through all unbonding entries and filter by participant
	iterator := unbondingStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var unbonding types.UnbondingCollateral
		k.cdc.MustUnmarshal(iterator.Value(), &unbonding)

		if unbonding.Participant == participantAddress.String() {
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
func (k Keeper) AdvanceEpoch(ctx context.Context, completedEpoch uint64) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	k.Logger().Info("advancing epoch in collateral module", "completed_epoch", completedEpoch)

	// Process unbonding queue for the epoch that just finished
	k.ProcessUnbondingQueue(sdkCtx, completedEpoch)

	// Increment the epoch counter
	nextEpoch := completedEpoch + 1
	k.SetCurrentEpoch(sdkCtx, nextEpoch)
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
		err = k.bookkeepingBankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, participantAddr, sdk.NewCoins(entry.Amount), "collateral unbonded")
		if err != nil {
			// This is a critical error, as it implies the module account is underfunded
			// which should not happen if logic is correct.
			panic(fmt.Sprintf("failed to release unbonding collateral for %s: %v", entry.Participant, err))
		}
		k.bookkeepingBankKeeper.LogSubAccountTransaction(ctx, entry.Participant, types.ModuleName, types.SubAccountUnbonding, entry.Amount, "collateral unbonded")

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

// GetAllUnbondings returns all unbonding entries (for genesis export)
func (k Keeper) GetAllUnbondings(ctx sdk.Context) []types.UnbondingCollateral {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	unbondingStore := prefix.NewStore(storeAdapter, types.UnbondingKey)
	unbondingList := []types.UnbondingCollateral{}

	iterator := unbondingStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var unbonding types.UnbondingCollateral
		k.cdc.MustUnmarshal(iterator.Value(), &unbonding)
		unbondingList = append(unbondingList, unbonding)
	}

	return unbondingList
}

// SetJailed stores a participant's jailed status.
// The presence of the key indicates the participant is jailed.
func (k Keeper) SetJailed(ctx sdk.Context, participantAddress sdk.AccAddress) {
	store := k.storeService.OpenKVStore(ctx)
	// We store a simple value, like a single byte, as the presence of the key is what matters.
	err := store.Set(types.GetJailedKey(participantAddress.String()), []byte{1})
	if err != nil {
		panic(err)
	}
}

// RemoveJailed removes a participant's jailed status.
func (k Keeper) RemoveJailed(ctx sdk.Context, participantAddress sdk.AccAddress) {
	store := k.storeService.OpenKVStore(ctx)
	err := store.Delete(types.GetJailedKey(participantAddress.String()))
	if err != nil {
		panic(err)
	}
}

// IsJailed checks if a participant is currently marked as jailed.
func (k Keeper) IsJailed(ctx sdk.Context, participantAddress sdk.AccAddress) bool {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(types.GetJailedKey(participantAddress.String()))
	if err != nil {
		panic(err)
	}
	return bz != nil
}

// GetAllJailed returns all jailed participant addresses.
func (k Keeper) GetAllJailed(ctx sdk.Context) []string {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	jailedStore := prefix.NewStore(storeAdapter, types.JailedKey)
	jailedList := []string{}

	iterator := jailedStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		// The key itself contains the address after the prefix.
		address := string(iterator.Key())
		jailedList = append(jailedList, address)
	}

	return jailedList
}

// Slash penalizes a participant by burning a fraction of their total collateral.
// This includes both their active collateral and any collateral in the unbonding queue.
// The slash is applied proportionally to all holdings.
func (k Keeper) Slash(ctx context.Context, participantAddress sdk.AccAddress, slashFraction math.LegacyDec) (sdk.Coin, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
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
	unbondingEntries := k.GetUnbondingByParticipant(sdkCtx, participantAddress)
	for _, entry := range unbondingEntries {
		slashAmountDec := math.LegacyNewDecFromInt(entry.Amount.Amount).Mul(slashFraction)
		slashAmount := sdk.NewCoin(entry.Amount.Denom, slashAmountDec.TruncateInt())

		if !slashAmount.IsZero() {
			newUnbondingAmount := entry.Amount.Sub(slashAmount)
			entry.Amount = newUnbondingAmount

			// If the unbonding entry is now zero, remove it. Otherwise, update it.
			if newUnbondingAmount.IsZero() {
				pAddr, err := sdk.AccAddressFromBech32(entry.Participant)
				if err != nil {
					// This should not happen if addresses are valid
					panic(fmt.Sprintf("invalid address in unbonding entry: %s", entry.Participant))
				}
				k.RemoveUnbondingCollateral(sdkCtx, pAddr, entry.CompletionEpoch)
			} else {
				k.setUnbondingCollateralEntry(sdkCtx, entry)
			}
			totalSlashedAmount = totalSlashedAmount.Add(slashAmount)
		}
	}

	// 3. Burn the total slashed amount from the module account
	if !totalSlashedAmount.IsZero() {
		err := k.bookkeepingBankKeeper.BurnCoins(sdkCtx, types.ModuleName, sdk.NewCoins(totalSlashedAmount), "collateral slashed")
		if err != nil {
			// This is a critical error, indicating an issue with the module account or supply
			return sdk.Coin{}, fmt.Errorf("failed to burn slashed coins: %w", err)
		}

		// 4. Emit a slash event
		sdkCtx.EventManager().EmitEvent(
			sdk.NewEvent(
				types.EventTypeSlashCollateral,
				sdk.NewAttribute(types.AttributeKeyParticipant, participantAddress.String()),
				sdk.NewAttribute(types.AttributeKeySlashAmount, totalSlashedAmount.String()),
				sdk.NewAttribute(types.AttributeKeySlashFraction, slashFraction.String()),
			),
		)

		k.Logger().Info("slashed participant collateral",
			"participant", participantAddress.String(),
			"slash_fraction", slashFraction.String(),
			"slashed_amount", totalSlashedAmount.String(),
		)
	}

	return totalSlashedAmount, nil
}
