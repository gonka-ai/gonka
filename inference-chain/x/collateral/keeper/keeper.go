package keeper

import (
	"context"
	"fmt"
	"strconv"

	"cosmossdk.io/collections"
	"cosmossdk.io/collections/indexes"
	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/collateral/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

type (
	// UnbondingIndexes groups the secondary indexes for the UnbondingCollateral map
	UnbondingIndexes struct {
		// ByParticipant indexes primary keys by participant address, to allow queries by participant
		ByParticipant *indexes.ReversePair[uint64, sdk.AccAddress, types.UnbondingCollateral]
	}

	Keeper struct {
		cdc          codec.BinaryCodec
		storeService store.KVStoreService
		logger       log.Logger

		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority string

		bankViewKeeper        types.BankKeeper
		bookkeepingBankKeeper types.BookkeepingBankKeeper
		params                collections.Item[types.Params]
		CollateralMap         collections.Map[sdk.AccAddress, sdk.Coin]
		Schema                collections.Schema
		CurrentEpoch          collections.Item[uint64]
		Jailed                collections.KeySet[sdk.AccAddress]
		// SlashedInEpoch tracks whether a participant has been slashed for a given reason in a given epoch
		SlashedInEpoch collections.KeySet[collections.Triple[uint64, sdk.AccAddress, string]]

		// UnbondingIM is an IndexedMap with primary key Pair[completionEpoch, participant]
		UnbondingIM collections.IndexedMap[collections.Pair[uint64, sdk.AccAddress], types.UnbondingCollateral, UnbondingIndexes]
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

	sb := collections.NewSchemaBuilder(storeService)
	unbondingIdx := UnbondingIndexes{
		ByParticipant: indexes.NewReversePair[types.UnbondingCollateral](
			sb,
			types.UnbondingByParticipantIndexPrefix,
			"unbonding_by_participant",
			collections.PairKeyCodec(collections.Uint64Key, sdk.AccAddressKey),
		),
	}

	ak := Keeper{
		cdc:          cdc,
		storeService: storeService,
		authority:    authority,
		logger:       logger,

		bankViewKeeper:        bankKeeper,
		bookkeepingBankKeeper: bookkeepingBankKeeper,
		params:                collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		CollateralMap:         collections.NewMap(sb, types.CollateralKey, "collateral", sdk.AccAddressKey, codec.CollValue[sdk.Coin](cdc)),
		CurrentEpoch:          collections.NewItem(sb, types.CurrentEpochKey, "current_epoch", collections.Uint64Value),
		Jailed:                collections.NewKeySet(sb, types.JailedKey, "jailed", sdk.AccAddressKey),
		SlashedInEpoch:        collections.NewKeySet(sb, types.SlashedInEpochKey, "slashed_in_epoch", collections.TripleKeyCodec(collections.Uint64Key, sdk.AccAddressKey, collections.StringKey)),
		UnbondingIM: *collections.NewIndexedMap(
			sb,
			types.UnbondingCollPrefix,
			"unbonding_collateral",
			collections.PairKeyCodec(collections.Uint64Key, sdk.AccAddressKey),
			codec.CollValue[types.UnbondingCollateral](cdc),
			unbondingIdx,
		),
	}
	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	ak.Schema = schema

	return ak
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
	err := k.CollateralMap.Set(ctx, participantAddress, amount)
	if err != nil {
		panic(err)
	}
}

// GetCollateral retrieves a participant's collateral amount
func (k Keeper) GetCollateral(ctx context.Context, participantAddress sdk.AccAddress) (sdk.Coin, bool) {
	coin, err := k.CollateralMap.Get(ctx, participantAddress)
	return coin, err == nil
}

// RemoveCollateral removes a participant's collateral from the store
func (k Keeper) RemoveCollateral(ctx context.Context, participantAddress sdk.AccAddress) {
	k.CollateralMap.Remove(ctx, participantAddress)
}

func (k Keeper) IterateCollaterals(ctx context.Context, process func(address sdk.AccAddress, amount sdk.Coin) (stop bool)) {
	err := k.CollateralMap.Walk(ctx, nil, func(address sdk.AccAddress, amount sdk.Coin) (bool, error) {
		return process(address, amount), nil
	})
	if err != nil {
		panic(err)
	}
}

// AddUnbondingCollateral stores an unbonding entry, adding to the amount if one already exists for the same participant and epoch.
func (k Keeper) AddUnbondingCollateral(ctx sdk.Context, participantAddress sdk.AccAddress, completionEpoch uint64, amount sdk.Coin) {
	pk := collections.Join(completionEpoch, participantAddress)
	// Check if an entry already exists for this epoch and participant
	existing, err := k.UnbondingIM.Get(ctx, pk)
	if err == nil {
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
	participantAddr, err := sdk.AccAddressFromBech32(unbonding.Participant)
	if err != nil {
		panic(err)
	}
	pk := collections.Join(unbonding.CompletionEpoch, participantAddr)
	if err := k.UnbondingIM.Set(ctx, pk, unbonding); err != nil {
		panic(err)
	}
}

// GetUnbondingCollateral retrieves a specific unbonding entry
func (k Keeper) GetUnbondingCollateral(ctx sdk.Context, participantAddress sdk.AccAddress, completionEpoch uint64) (types.UnbondingCollateral, bool) {
	pk := collections.Join(completionEpoch, participantAddress)
	val, err := k.UnbondingIM.Get(ctx, pk)
	if err != nil {
		return types.UnbondingCollateral{}, false
	}
	return val, true
}

// RemoveUnbondingCollateral removes an unbonding entry
func (k Keeper) RemoveUnbondingCollateral(ctx sdk.Context, participantAddress sdk.AccAddress, completionEpoch uint64) {
	pk := collections.Join(completionEpoch, participantAddress)
	if err := k.UnbondingIM.Remove(ctx, pk); err != nil {
		panic(err)
	}
}

// RemoveUnbondingByEpoch removes all unbonding entries for a specific epoch
// This is useful for batch processing at the end of an epoch
func (k Keeper) RemoveUnbondingByEpoch(ctx sdk.Context, completionEpoch uint64) {
	iter, err := k.UnbondingIM.Iterate(ctx, collections.NewPrefixedPairRange[uint64, sdk.AccAddress](completionEpoch))
	if err != nil {
		panic(err)
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		pk, err := iter.Key()
		if err != nil {
			panic(err)
		}
		if err := k.UnbondingIM.Remove(ctx, pk); err != nil {
			panic(err)
		}
	}
}

// GetUnbondingByEpoch returns all unbonding entries for a specific epoch
func (k Keeper) GetUnbondingByEpoch(ctx sdk.Context, completionEpoch uint64) []types.UnbondingCollateral {
	iter, err := k.UnbondingIM.Iterate(ctx, collections.NewPrefixedPairRange[uint64, sdk.AccAddress](completionEpoch))
	if err != nil {
		panic(err)
	}
	defer iter.Close()
	var list []types.UnbondingCollateral
	for ; iter.Valid(); iter.Next() {
		v, err := iter.Value()
		if err != nil {
			panic(err)
		}
		list = append(list, v)
	}
	return list
}

// GetUnbondingByParticipant returns all unbonding entries for a specific participant
func (k Keeper) GetUnbondingByParticipant(ctx sdk.Context, participantAddress sdk.AccAddress) []types.UnbondingCollateral {
	idxIter, err := k.UnbondingIM.Indexes.ByParticipant.MatchExact(ctx, participantAddress)
	if err != nil {
		panic(err)
	}
	defer idxIter.Close()
	var list []types.UnbondingCollateral
	for ; idxIter.Valid(); idxIter.Next() {
		pk, err := idxIter.PrimaryKey()
		if err != nil {
			panic(err)
		}
		v, err := k.UnbondingIM.Get(ctx, pk)
		if err != nil {
			panic(err)
		}
		list = append(list, v)
	}
	return list
}

// GetCurrentEpoch retrieves the current epoch from the store.
func (k Keeper) GetCurrentEpoch(ctx sdk.Context) uint64 {
	value, err := k.CurrentEpoch.Get(ctx)
	if err != nil {
		panic(err)
	}
	return value
}

// SetCurrentEpoch sets the current epoch in the store.
func (k Keeper) SetCurrentEpoch(ctx sdk.Context, epoch uint64) {
	k.Logger().Info("Setting current epoch in collateral module", "epoch", epoch)
	err := k.CurrentEpoch.Set(ctx, epoch)
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
	iter, err := k.UnbondingIM.Iterate(ctx, nil)
	if err != nil {
		panic(err)
	}
	defer iter.Close()
	var list []types.UnbondingCollateral
	for ; iter.Valid(); iter.Next() {
		v, err := iter.Value()
		if err != nil {
			panic(err)
		}
		list = append(list, v)
	}
	return list
}

// SetJailed stores a participant's jailed status.
// The presence of the key indicates the participant is jailed.
func (k Keeper) SetJailed(ctx sdk.Context, participantAddress sdk.AccAddress) {
	err := k.Jailed.Set(ctx, participantAddress)
	if err != nil {
		panic(err)
	}
}

// RemoveJailed removes a participant's jailed status.
func (k Keeper) RemoveJailed(ctx sdk.Context, participantAddress sdk.AccAddress) {
	err := k.Jailed.Remove(ctx, participantAddress)
	if err != nil {
		panic(err)
	}
}

// IsJailed checks if a participant is currently marked as jailed.
func (k Keeper) IsJailed(ctx sdk.Context, participantAddress sdk.AccAddress) bool {
	found, err := k.Jailed.Has(ctx, participantAddress)
	if err != nil {
		panic(err)
	}
	return found
}

// GetAllJailed returns all jailed participant addresses.
func (k Keeper) GetAllJailed(ctx sdk.Context) []sdk.AccAddress {
	iter, err := k.Jailed.Iterate(ctx, nil)
	if err != nil {
		panic(err)
	}
	result, err := iter.Keys()
	if err != nil {
		panic(err)
	}
	return result
}

// Slash penalizes a participant by burning a fraction of their total collateral.
// This includes both their active collateral and any collateral in the unbonding queue.
// The slash is applied proportionally to all holdings.
func (k Keeper) Slash(ctx context.Context, participantAddress sdk.AccAddress, slashFraction math.LegacyDec, reason string) (sdk.Coin, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if slashFraction.IsNegative() || slashFraction.GT(math.LegacyOneDec()) {
		return sdk.Coin{}, fmt.Errorf("slash fraction must be between 0 and 1, got %s", slashFraction)
	}
	key, err := k.checkIfAlreadySlashed(ctx, participantAddress, reason)
	if err != nil {
		return sdk.Coin{}, err
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
		err := k.bookkeepingBankKeeper.BurnCoins(sdkCtx, types.ModuleName, sdk.NewCoins(totalSlashedAmount), "collateral_slashed:"+reason)
		if err != nil {
			// This is a critical error, indicating an issue with the module account or supply
			return sdk.Coin{}, fmt.Errorf("failed to burn slashed coins: %w", err)
		}

		if key != nil {
			// Mark as slashed for this epoch+participant+reason now that slash succeeded
			if err := k.SlashedInEpoch.Set(sdkCtx, *key); err != nil {
				return sdk.Coin{}, err
			}
		}

		// 4. Emit a slash event
		sdkCtx.EventManager().EmitEvent(
			sdk.NewEvent(
				types.EventTypeSlashCollateral,
				sdk.NewAttribute(types.AttributeKeyParticipant, participantAddress.String()),
				sdk.NewAttribute(types.AttributeKeySlashAmount, totalSlashedAmount.String()),
				sdk.NewAttribute(types.AttributeKeySlashFraction, slashFraction.String()),
				sdk.NewAttribute(types.AttributeKeySlashReason, reason),
			),
		)

		k.Logger().Info("slashed participant collateral",
			"participant", participantAddress.String(),
			"slash_fraction", slashFraction.String(),
			"slashed_amount", totalSlashedAmount.String(),
			"reason", reason,
		)
	}

	return totalSlashedAmount, nil
}

func (k Keeper) checkIfAlreadySlashed(ctx context.Context, participantAddress sdk.AccAddress, reason string) (*collections.Triple[uint64, sdk.AccAddress, string], error) {
	if reason == "" {
		return nil, nil
	}
	// get current epoch; default to 0 if not set yet
	epoch := uint64(0)
	if v, err := k.CurrentEpoch.Get(ctx); err == nil {
		epoch = v
	}
	// Check duplicate slashing for this (epoch, participant, reason)
	var tripleKey = collections.Join3(epoch, participantAddress, reason)
	found, err := k.SlashedInEpoch.Has(ctx, tripleKey)
	if err != nil {
		return nil, err
	}
	if found {
		return nil, fmt.Errorf("already slashed for epoch=%d reason=%s", epoch, reason)
	}
	return &tripleKey, nil
}
