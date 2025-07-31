package keeper

import (
	"fmt"

	"context"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/streamvesting/types"
)

type (
	Keeper struct {
		cdc          codec.BinaryCodec
		storeService store.KVStoreService
		logger       log.Logger

		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority string

		bankKeeper            types.BankKeeper
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

		bankKeeper:            bankKeeper,
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

const (
	HoldingSubAccount = "vesting"
)

// AddVestedRewards adds vested rewards to a participant's schedule with aggregation logic
func (k Keeper) AddVestedRewards(ctx context.Context, participantAddress string, fundingModule string, amount sdk.Coins, vestingEpochs *uint64, memo string) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	err := k.bookkeepingBankKeeper.SendCoinsFromModuleToModule(ctx, fundingModule, types.ModuleName, amount, memo)
	if err != nil {
		return fmt.Errorf("failed to transfer coins from module %s to streamvesting module: %w", fundingModule, err)
	}
	for _, coin := range amount {
		k.bookkeepingBankKeeper.LogSubAccountTransaction(ctx, types.ModuleName, participantAddress, HoldingSubAccount,
			coin, "vesting started for "+participantAddress)
	}

	// Determine vesting epochs - use parameter if not specified
	var epochs uint64
	if vestingEpochs != nil {
		epochs = *vestingEpochs
	} else {
		params := k.GetParams(sdkCtx)
		epochs = params.RewardVestingPeriod
	}

	if epochs == 0 {
		return fmt.Errorf("vesting epochs cannot be zero")
	}

	if amount.IsZero() {
		return nil // Nothing to vest, return successfully
	}

	// Validate participant address
	_, err = sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return fmt.Errorf("invalid participant address: %w", err)
	}

	// Get or create vesting schedule
	schedule, found := k.GetVestingSchedule(sdkCtx, participantAddress)
	if !found {
		schedule = types.VestingSchedule{
			ParticipantAddress: participantAddress,
			EpochAmounts:       []types.EpochCoins{},
		}
	}

	// Extend the schedule if necessary
	requiredLength := int(epochs)
	for len(schedule.EpochAmounts) < requiredLength {
		schedule.EpochAmounts = append(schedule.EpochAmounts, types.EpochCoins{
			Coins: sdk.NewCoins(),
		})
	}

	// Implement aggregation logic for each coin denomination
	for _, coin := range amount {
		// Divide amount by epochs
		epochsInt := math.NewInt(int64(epochs))
		amountPerEpoch := coin.Amount.Quo(epochsInt)
		remainder := coin.Amount.Mod(epochsInt)

		// Add the base amount to each epoch
		for i := 0; i < int(epochs); i++ {
			epochCoin := sdk.NewCoin(coin.Denom, amountPerEpoch)

			// Add remainder to the first epoch
			if i == 0 && !remainder.IsZero() {
				epochCoin = epochCoin.Add(sdk.NewCoin(coin.Denom, remainder))
			}

			// Add to existing amount in this epoch
			schedule.EpochAmounts[i].Coins = schedule.EpochAmounts[i].Coins.Add(epochCoin)
		}
	}

	// Store the updated schedule
	k.SetVestingSchedule(sdkCtx, schedule)

	// Emit event for reward vesting
	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeVestReward,
			sdk.NewAttribute(types.AttributeKeyParticipant, participantAddress),
			sdk.NewAttribute(types.AttributeKeyAmount, amount.String()),
			sdk.NewAttribute(types.AttributeKeyVestingEpochs, fmt.Sprintf("%d", epochs)),
		),
	)

	return nil
}

// AdvanceEpoch is called by the inference module when an epoch completes.
// It triggers the unlocking of vested tokens for all participants
func (k Keeper) AdvanceEpoch(ctx context.Context, completedEpoch uint64) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	k.Logger().Info("Processing epoch advancement for streamvesting", "epoch", completedEpoch)

	// Process token unlocks for the completed epoch
	err := k.ProcessEpochUnlocks(sdkCtx)
	if err != nil {
		k.Logger().Error("Failed to process epoch unlocks", "epoch", completedEpoch, "error", err)
		return fmt.Errorf("failed to process epoch unlocks for epoch %d: %w", completedEpoch, err)
	}

	k.Logger().Info("Completed epoch advancement for streamvesting", "epoch", completedEpoch)
	return nil
}

// ProcessEpochUnlocks processes all vesting schedules and unlocks the first epoch's tokens
func (k Keeper) ProcessEpochUnlocks(ctx sdk.Context) error {
	// Get all vesting schedules
	schedules := k.GetAllVestingSchedules(ctx)

	// Track totals for summary event
	totalUnlocked := sdk.NewCoins()
	participantsProcessed := 0
	participantsUnlocked := 0

	for _, schedule := range schedules {
		participantsProcessed++

		// Skip if no epochs to unlock
		if len(schedule.EpochAmounts) == 0 {
			continue
		}

		// Get the first epoch's coins to unlock
		coinsToUnlock := schedule.EpochAmounts[0].Coins

		// Skip if no coins to unlock
		if coinsToUnlock.IsZero() {
			// Remove the empty first epoch and continue
			schedule.EpochAmounts = schedule.EpochAmounts[1:]

			// Update or delete the schedule
			if len(schedule.EpochAmounts) == 0 {
				k.RemoveVestingSchedule(ctx, schedule.ParticipantAddress)
			} else {
				k.SetVestingSchedule(ctx, schedule)
			}
			continue
		}

		// Transfer coins from module account to participant
		participantAddr, err := sdk.AccAddressFromBech32(schedule.ParticipantAddress)
		if err != nil {
			k.Logger().Error("Invalid participant address", "address", schedule.ParticipantAddress, "error", err)
			continue
		}

		err = k.bookkeepingBankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, participantAddr, coinsToUnlock, "vesting payment")
		if err != nil {
			k.Logger().Error("Failed to unlock vested tokens", "participant", schedule.ParticipantAddress, "amount", coinsToUnlock, "error", err)
			continue
		}

		// Remove the first epoch from the schedule
		schedule.EpochAmounts = schedule.EpochAmounts[1:]

		// Update or delete the schedule based on remaining epochs
		if len(schedule.EpochAmounts) == 0 {
			k.RemoveVestingSchedule(ctx, schedule.ParticipantAddress)
		} else {
			k.SetVestingSchedule(ctx, schedule)
		}
		for _, coin := range coinsToUnlock {
			k.bookkeepingBankKeeper.LogSubAccountTransaction(
				ctx, schedule.ParticipantAddress, types.ModuleName, HoldingSubAccount, coin, "coins vested for "+schedule.ParticipantAddress)
		}

		// Add to totals
		totalUnlocked = totalUnlocked.Add(coinsToUnlock...)
		participantsUnlocked++

		k.Logger().Info("Unlocked vested tokens", "participant", schedule.ParticipantAddress, "amount", coinsToUnlock)
	}

	// Emit single summary event for the entire epoch unlock process
	if participantsUnlocked > 0 {
		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				types.EventTypeUnlockTokens,
				sdk.NewAttribute(types.AttributeKeyUnlockedAmount, totalUnlocked.String()),
				sdk.NewAttribute("participants_unlocked", fmt.Sprintf("%d", participantsUnlocked)),
				sdk.NewAttribute("participants_processed", fmt.Sprintf("%d", participantsProcessed)),
			),
		)

		k.Logger().Info("Epoch vesting unlock completed",
			"total_unlocked", totalUnlocked,
			"participants_unlocked", participantsUnlocked,
			"participants_processed", participantsProcessed)
	}

	return nil
}

// SetVestingSchedule stores a vesting schedule for a participant
func (k Keeper) SetVestingSchedule(ctx sdk.Context, schedule types.VestingSchedule) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.VestingScheduleKeyPrefix)
	key := []byte(schedule.ParticipantAddress)
	value := k.cdc.MustMarshal(&schedule)
	store.Set(key, value)
}

// GetVestingSchedule retrieves a vesting schedule for a participant
func (k Keeper) GetVestingSchedule(ctx sdk.Context, participantAddress string) (schedule types.VestingSchedule, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.VestingScheduleKeyPrefix)
	key := []byte(participantAddress)
	value := store.Get(key)

	if value == nil {
		return schedule, false
	}

	k.cdc.MustUnmarshal(value, &schedule)
	return schedule, true
}

// RemoveVestingSchedule removes a vesting schedule for a participant
func (k Keeper) RemoveVestingSchedule(ctx sdk.Context, participantAddress string) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.VestingScheduleKeyPrefix)
	key := []byte(participantAddress)
	store.Delete(key)
}

// GetAllVestingSchedules retrieves all vesting schedules
func (k Keeper) GetAllVestingSchedules(ctx sdk.Context) []types.VestingSchedule {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.VestingScheduleKeyPrefix)
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})
	defer iterator.Close()

	var schedules []types.VestingSchedule
	for ; iterator.Valid(); iterator.Next() {
		var schedule types.VestingSchedule
		k.cdc.MustUnmarshal(iterator.Value(), &schedule)
		schedules = append(schedules, schedule)
	}

	return schedules
}
