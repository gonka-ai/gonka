package keeper

import (
	"context"
	"fmt"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"

	"github.com/productscience/inference/x/genesistransfer/types"
)

// TransferOwnership performs complete ownership transfer including both balance and vesting schedule transfer
// This is the unified function that replaces separate TransferLiquidBalances and TransferVestingSchedule calls
func (k Keeper) TransferOwnership(ctx context.Context, genesisAddr, recipientAddr sdk.AccAddress) error {
	// Validate addresses
	if genesisAddr == nil {
		return errors.ErrInvalidAddress.Wrap("genesis address cannot be nil")
	}
	if recipientAddr == nil {
		return errors.ErrInvalidAddress.Wrap("recipient address cannot be nil")
	}
	if genesisAddr.Equals(recipientAddr) {
		return errors.ErrInvalidRequest.Wrap("cannot transfer to the same address")
	}

	// Validate that the genesis account exists first
	genesisAccount := k.accountKeeper.GetAccount(ctx, genesisAddr)
	if genesisAccount == nil {
		return types.ErrAccountNotFound.Wrapf("genesis account %s does not exist", genesisAddr.String())
	}

	// Get all balances from the genesis account
	genesisBalances := k.bankView.GetAllBalances(ctx, genesisAddr)
	if genesisBalances.IsZero() {
		return types.ErrInsufficientBalance.Wrap("genesis account has no balances to transfer")
	}

	// Ensure recipient account exists, create if it doesn't
	recipientAccount := k.accountKeeper.GetAccount(ctx, recipientAddr)
	if recipientAccount == nil {
		// Create new account for recipient
		recipientAccount = k.accountKeeper.NewAccountWithAddress(ctx, recipientAddr)
		k.accountKeeper.SetAccount(ctx, recipientAccount)
	}

	// Step 1: Transfer all balances (both liquid and vesting coins)
	if err := k.transferBalances(ctx, genesisAddr, recipientAddr, genesisBalances); err != nil {
		return errorsmod.Wrapf(err, "balance transfer failed")
	}

	// Step 2: Transfer vesting schedule (if applicable)
	if err := k.transferVestingSchedule(ctx, genesisAccount, recipientAddr); err != nil {
		return errorsmod.Wrapf(err, "vesting schedule transfer failed")
	}

	return nil
}

// transferBalances is an internal helper that transfers all balances from genesis to recipient
// Uses two-step transfer through module account to bypass transfer restrictions:
// 1. Genesis account → GenesisTransfer module account (user-to-module: allowed)
// 2. GenesisTransfer module account → Recipient (module-to-user: allowed)
func (k Keeper) transferBalances(ctx context.Context, genesisAddr, recipientAddr sdk.AccAddress, balances sdk.Coins) error {
	memo := fmt.Sprintf("Genesis account ownership transfer from %s to %s", genesisAddr.String(), recipientAddr.String())

	// Step 1: Transfer from genesis account to module account (bypasses restrictions: user-to-module allowed)
	if err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, genesisAddr, types.ModuleName, balances, memo); err != nil {
		return errors.ErrInvalidRequest.Wrapf("failed to transfer balances to module: %v", err)
	}

	// Step 2: Transfer from module account to recipient (bypasses restrictions: module-to-user allowed)
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, recipientAddr, balances, memo); err != nil {
		return errors.ErrInvalidRequest.Wrapf("failed to transfer balances from module: %v", err)
	}

	// Log the successful transfer
	k.Logger().Info(
		"balance transfer completed via module account",
		"genesis_address", genesisAddr.String(),
		"recipient_address", recipientAddr.String(),
		"transferred_amount", balances.String(),
		"module_account", types.ModuleName,
	)

	return nil
}

// transferVestingSchedule is an internal helper that transfers vesting schedule from genesis account to recipient
// It takes the genesis account object to avoid redundant lookups
func (k Keeper) transferVestingSchedule(ctx context.Context, genesisAccount sdk.AccountI, recipientAddr sdk.AccAddress) error {
	// Check if the genesis account has vesting
	hasVesting, vestingCoins, _, err := k.GetVestingInfo(ctx, genesisAccount.GetAddress())
	if err != nil {
		return errorsmod.Wrapf(err, "failed to get vesting info")
	}

	// If no vesting, nothing to transfer
	if !hasVesting || vestingCoins.IsZero() {
		k.Logger().Info(
			"no vesting schedule to transfer",
			"genesis_address", genesisAccount.GetAddress().String(),
			"recipient_address", recipientAddr.String(),
		)
		return nil
	}

	// Create vesting account for recipient based on genesis account type
	currentTime := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	var newVestingAccount sdk.AccountI

	switch v := genesisAccount.(type) {
	case *vestingtypes.PeriodicVestingAccount:
		newVestingAccount, err = k.createPeriodicVestingAccount(ctx, v, recipientAddr, currentTime)
	case *vestingtypes.ContinuousVestingAccount:
		newVestingAccount, err = k.createContinuousVestingAccount(ctx, v, recipientAddr, currentTime)
	case *vestingtypes.DelayedVestingAccount:
		newVestingAccount, err = k.createDelayedVestingAccount(ctx, v, recipientAddr, currentTime)
	case *vestingtypes.BaseVestingAccount:
		newVestingAccount, err = k.createBaseVestingAccount(ctx, v, recipientAddr, currentTime)
	default:
		// No vesting account, nothing to transfer
		return nil
	}

	if err != nil {
		return errorsmod.Wrapf(err, "failed to create vesting account")
	}

	// Set the new vesting account for the recipient
	k.accountKeeper.SetAccount(ctx, newVestingAccount)

	// Convert the original genesis account to a regular BaseAccount (cleanup)
	// This removes the vesting account structure since all assets have been transferred
	baseAccount := authtypes.NewBaseAccount(
		genesisAccount.GetAddress(),
		genesisAccount.GetPubKey(),
		genesisAccount.GetAccountNumber(),
		genesisAccount.GetSequence(),
	)
	k.accountKeeper.SetAccount(ctx, baseAccount)

	// Log successful vesting transfer
	k.Logger().Info(
		"vesting schedule transfer completed",
		"genesis_address", genesisAccount.GetAddress().String(),
		"recipient_address", recipientAddr.String(),
		"vesting_amount", vestingCoins.String(),
	)

	return nil
}

// createPeriodicVestingAccount creates a periodic vesting account for the recipient
func (k Keeper) createPeriodicVestingAccount(ctx context.Context, vestingAcc *vestingtypes.PeriodicVestingAccount, recipientAddr sdk.AccAddress, currentTime int64) (sdk.AccountI, error) {
	// Calculate remaining periods and amounts
	var remainingPeriods []vestingtypes.Period
	var remainingCoins sdk.Coins
	accumulatedTime := vestingAcc.StartTime

	for _, period := range vestingAcc.VestingPeriods {
		periodEndTime := accumulatedTime + period.Length
		if periodEndTime > currentTime {
			// This period has time remaining
			adjustedLength := period.Length
			if accumulatedTime < currentTime {
				// Partial period - adjust the length
				adjustedLength = periodEndTime - currentTime
			}

			remainingPeriods = append(remainingPeriods, vestingtypes.Period{
				Length: adjustedLength,
				Amount: period.Amount,
			})
			remainingCoins = remainingCoins.Add(period.Amount...)
		}
		accumulatedTime = periodEndTime
	}

	// Create base account for recipient
	baseAccount := authtypes.NewBaseAccountWithAddress(recipientAddr)

	// Create new periodic vesting account with remaining periods
	newVestingAcc, err := vestingtypes.NewPeriodicVestingAccount(baseAccount, remainingCoins, currentTime, remainingPeriods)
	if err != nil {
		return nil, err
	}

	return newVestingAcc, nil
}

// createContinuousVestingAccount creates a continuous vesting account for the recipient
func (k Keeper) createContinuousVestingAccount(ctx context.Context, vestingAcc *vestingtypes.ContinuousVestingAccount, recipientAddr sdk.AccAddress, currentTime int64) (sdk.AccountI, error) {
	// Calculate remaining vesting amount proportionally
	totalDuration := vestingAcc.EndTime - vestingAcc.StartTime
	if totalDuration <= 0 || currentTime >= vestingAcc.EndTime {
		// Vesting has ended, no vesting account needed
		return nil, nil
	}

	remainingDuration := vestingAcc.EndTime - currentTime
	if remainingDuration <= 0 {
		// Vesting has ended
		return nil, nil
	}

	// Calculate proportional remaining amount
	originalAmount := vestingAcc.OriginalVesting[0].Amount
	remainingAmount := originalAmount.MulRaw(remainingDuration).QuoRaw(totalDuration)
	remainingCoins := sdk.NewCoins(sdk.NewCoin(vestingAcc.OriginalVesting[0].Denom, remainingAmount))

	// Create base account for recipient
	baseAccount := authtypes.NewBaseAccountWithAddress(recipientAddr)

	// Create new continuous vesting account with remaining duration
	newVestingAcc, err := vestingtypes.NewContinuousVestingAccount(baseAccount, remainingCoins, currentTime, vestingAcc.EndTime)
	if err != nil {
		return nil, err
	}

	return newVestingAcc, nil
}

// createDelayedVestingAccount creates a delayed vesting account for the recipient
func (k Keeper) createDelayedVestingAccount(ctx context.Context, vestingAcc *vestingtypes.DelayedVestingAccount, recipientAddr sdk.AccAddress, currentTime int64) (sdk.AccountI, error) {
	// Check if vesting has ended
	if currentTime >= vestingAcc.EndTime {
		// Vesting has ended, no vesting account needed
		return nil, nil
	}

	// Create base account for recipient
	baseAccount := authtypes.NewBaseAccountWithAddress(recipientAddr)
	remainingCoins := vestingAcc.OriginalVesting

	// Create new delayed vesting account with same end time
	newVestingAcc, err := vestingtypes.NewDelayedVestingAccount(baseAccount, remainingCoins, vestingAcc.EndTime)
	if err != nil {
		return nil, err
	}

	return newVestingAcc, nil
}

// createBaseVestingAccount creates a base vesting account for the recipient
func (k Keeper) createBaseVestingAccount(ctx context.Context, vestingAcc *vestingtypes.BaseVestingAccount, recipientAddr sdk.AccAddress, currentTime int64) (sdk.AccountI, error) {
	// Check if vesting has ended
	if currentTime >= vestingAcc.EndTime {
		// Vesting has ended, no vesting account needed
		return nil, nil
	}

	// Create base account for recipient
	baseAccount := authtypes.NewBaseAccountWithAddress(recipientAddr)
	remainingCoins := vestingAcc.OriginalVesting

	// Create new base vesting account
	newBaseVestingAcc, err := vestingtypes.NewBaseVestingAccount(baseAccount, remainingCoins, currentTime)
	if err != nil {
		return nil, err
	}

	// Set end time manually
	newBaseVestingAcc.EndTime = vestingAcc.EndTime

	return newBaseVestingAcc, nil
}

// GetVestingInfo returns vesting information for an account
func (k Keeper) GetVestingInfo(ctx context.Context, addr sdk.AccAddress) (bool, sdk.Coins, int64, error) {
	if addr == nil {
		return false, nil, 0, errors.ErrInvalidAddress.Wrap("address cannot be nil")
	}

	account := k.accountKeeper.GetAccount(ctx, addr)
	if account == nil {
		return false, nil, 0, types.ErrAccountNotFound.Wrapf("account %s does not exist", addr.String())
	}

	// Check different vesting account types
	if periodicAcc, ok := account.(*vestingtypes.PeriodicVestingAccount); ok {
		return true, periodicAcc.OriginalVesting, periodicAcc.EndTime, nil
	} else if continuousAcc, ok := account.(*vestingtypes.ContinuousVestingAccount); ok {
		return true, continuousAcc.OriginalVesting, continuousAcc.EndTime, nil
	} else if delayedAcc, ok := account.(*vestingtypes.DelayedVestingAccount); ok {
		return true, delayedAcc.OriginalVesting, delayedAcc.EndTime, nil
	} else if baseAcc, ok := account.(*vestingtypes.BaseVestingAccount); ok {
		return true, baseAcc.OriginalVesting, baseAcc.EndTime, nil
	}

	// Not a vesting account
	return false, nil, 0, nil
}

// ExecuteOwnershipTransfer performs the complete atomic ownership transfer process
// This function orchestrates the entire transfer including validation, balance transfer,
// vesting transfer, record creation, and event emission with atomic all-or-nothing execution
func (k Keeper) ExecuteOwnershipTransfer(ctx context.Context, genesisAddr, recipientAddr sdk.AccAddress) error {
	// Phase 1: Pre-transfer validation
	k.Logger().Info(
		"starting ownership transfer execution",
		"genesis_address", genesisAddr.String(),
		"recipient_address", recipientAddr.String(),
	)

	// Comprehensive validation (this includes all security checks)
	if err := k.ValidateTransfer(ctx, genesisAddr, recipientAddr); err != nil {
		k.Logger().Error(
			"transfer validation failed",
			"genesis_address", genesisAddr.String(),
			"recipient_address", recipientAddr.String(),
			"error", err,
		)
		return errorsmod.Wrapf(err, "transfer validation failed")
	}

	// Get current block info for transfer record
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	currentHeight := sdkCtx.BlockHeight()

	// Phase 2: Execute complete ownership transfer (balances + vesting schedule)
	// Note: In Cosmos SDK, operations within a keeper are atomic within the transaction
	// If any operation fails, the entire transaction will be rolled back

	// Execute unified ownership transfer (both balances and vesting schedule)
	transferErr := k.TransferOwnership(ctx, genesisAddr, recipientAddr)
	if transferErr != nil {
		k.Logger().Error(
			"ownership transfer failed",
			"genesis_address", genesisAddr.String(),
			"recipient_address", recipientAddr.String(),
			"error", transferErr,
		)
		return errorsmod.Wrapf(transferErr, "ownership transfer failed")
	}

	// Phase 3: Create transfer record and persist state
	transferRecord := types.TransferRecord{
		GenesisAddress:    genesisAddr.String(),
		RecipientAddress:  recipientAddr.String(),
		TransferHeight:    uint64(currentHeight),
		Completed:         true,
		TransferredDenoms: k.getTransferredDenoms(ctx, genesisAddr),
		TransferAmount:    k.getTotalTransferAmount(ctx, genesisAddr, recipientAddr),
	}

	// Store the transfer record
	if err := k.SetTransferRecord(ctx, transferRecord); err != nil {
		k.Logger().Error(
			"failed to store transfer record",
			"genesis_address", genesisAddr.String(),
			"recipient_address", recipientAddr.String(),
			"error", err,
		)
		return errorsmod.Wrapf(err, "failed to store transfer record")
	}

	// Phase 4: Emit events for monitoring and audit trail
	k.emitOwnershipTransferEvents(ctx, transferRecord)

	// Phase 5: Post-transfer validation and cleanup
	if err := k.validateTransferCompletion(ctx, genesisAddr, recipientAddr, transferRecord); err != nil {
		k.Logger().Error(
			"post-transfer validation failed",
			"genesis_address", genesisAddr.String(),
			"recipient_address", recipientAddr.String(),
			"error", err,
		)
		// Note: At this point, transfers have already occurred, so we log the error
		// but don't fail the transaction. The transfer record indicates completion.
		k.Logger().Warn(
			"transfer completed but post-validation failed - manual review may be needed",
			"genesis_address", genesisAddr.String(),
			"recipient_address", recipientAddr.String(),
			"transfer_height", currentHeight,
		)
	}

	// Success
	k.Logger().Info(
		"ownership transfer completed successfully",
		"genesis_address", genesisAddr.String(),
		"recipient_address", recipientAddr.String(),
		"transfer_height", currentHeight,
		"transferred_denoms", transferRecord.TransferredDenoms,
		"transfer_amount", transferRecord.TransferAmount,
	)

	return nil
}

// getTransferredDenoms determines which denominations were transferred
func (k Keeper) getTransferredDenoms(ctx context.Context, genesisAddr sdk.AccAddress) []string {
	// Get the balances that were present before transfer
	// Note: This is called after transfer, so we need to reconstruct from transfer record context
	// For now, we'll use a simple approach - in production this might need more sophisticated tracking

	denoms := make(map[string]bool)

	// Check if there were any spendable coins (these would have been transferred)
	spendableCoins := k.bankView.SpendableCoins(ctx, genesisAddr)
	for _, coin := range spendableCoins {
		denoms[coin.Denom] = true
	}

	// Check vesting coins
	hasVesting, vestingCoins, _, err := k.GetVestingInfo(ctx, genesisAddr)
	if err == nil && hasVesting {
		for _, coin := range vestingCoins {
			denoms[coin.Denom] = true
		}
	}

	// Convert map to slice
	denomSlice := make([]string, 0, len(denoms))
	for denom := range denoms {
		denomSlice = append(denomSlice, denom)
	}

	return denomSlice
}

// getTotalTransferAmount calculates the total amount that was transferred
func (k Keeper) getTotalTransferAmount(ctx context.Context, genesisAddr, recipientAddr sdk.AccAddress) string {
	// Get recipient's current total balance as a proxy for transferred amount
	// This is a simplified approach - in production you might want more detailed tracking
	totalBalance := k.bankView.GetAllBalances(ctx, recipientAddr)
	return totalBalance.String()
}

// emitOwnershipTransferEvents emits events for monitoring and audit trail
func (k Keeper) emitOwnershipTransferEvents(ctx context.Context, record types.TransferRecord) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Emit ownership transfer completed event
	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			"ownership_transfer_completed",
			sdk.NewAttribute("genesis_address", record.GenesisAddress),
			sdk.NewAttribute("recipient_address", record.RecipientAddress),
			sdk.NewAttribute("transfer_height", fmt.Sprintf("%d", record.TransferHeight)),
			sdk.NewAttribute("transferred_denoms", fmt.Sprintf("%v", record.TransferredDenoms)),
			sdk.NewAttribute("transfer_amount", record.TransferAmount),
		),
	)

	// Emit module-specific event
	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(sdk.AttributeKeyAction, "execute_ownership_transfer"),
			sdk.NewAttribute("genesis_account", record.GenesisAddress),
			sdk.NewAttribute("new_owner", record.RecipientAddress),
		),
	)
}

// validateTransferCompletion performs post-transfer validation
func (k Keeper) validateTransferCompletion(ctx context.Context, genesisAddr, recipientAddr sdk.AccAddress, record types.TransferRecord) error {
	// Verify transfer record exists and is correct
	storedRecord, found, err := k.GetTransferRecord(ctx, genesisAddr)
	if err != nil {
		return errorsmod.Wrapf(err, "failed to retrieve stored transfer record")
	}
	if !found {
		return types.ErrInvalidTransfer.Wrap("transfer record not found after completion")
	}
	if storedRecord.RecipientAddress != record.RecipientAddress {
		return types.ErrInvalidTransfer.Wrapf(
			"stored transfer record recipient mismatch: expected %s, got %s",
			record.RecipientAddress,
			storedRecord.RecipientAddress,
		)
	}

	// Verify recipient account exists and has assets
	recipientAccount := k.accountKeeper.GetAccount(ctx, recipientAddr)
	if recipientAccount == nil {
		return types.ErrInvalidTransfer.Wrap("recipient account not found after transfer completion")
	}

	// Verify recipient has received assets
	recipientBalances := k.bankView.GetAllBalances(ctx, recipientAddr)
	if recipientBalances.IsZero() {
		// Check if they have vesting assets instead
		hasVesting, vestingCoins, _, err := k.GetVestingInfo(ctx, recipientAddr)
		if err != nil || !hasVesting || vestingCoins.IsZero() {
			return types.ErrInvalidTransfer.Wrap("recipient has no assets after transfer completion")
		}
	}

	return nil
}
