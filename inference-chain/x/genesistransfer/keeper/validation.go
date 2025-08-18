package keeper

import (
	"context"
	"fmt"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/errors"

	"github.com/productscience/inference/x/genesistransfer/types"
)

// ValidateTransfer performs comprehensive validation for ownership transfers
// This function ensures transfer eligibility and security by validating account ownership,
// existence, balance verification, transfer completion history, and optional whitelist validation
func (k Keeper) ValidateTransfer(ctx context.Context, genesisAddr, recipientAddr sdk.AccAddress) error {
	// Basic address validation
	if err := k.validateAddresses(genesisAddr, recipientAddr); err != nil {
		return err
	}

	// Check if transfer has already been completed (one-time enforcement)
	if err := k.validateTransferHistory(ctx, genesisAddr); err != nil {
		return err
	}

	// Validate account existence and ownership
	if err := k.validateAccountExistence(ctx, genesisAddr, recipientAddr); err != nil {
		return err
	}

	// Validate account balance (ensure there's something to transfer)
	if err := k.validateAccountBalance(ctx, genesisAddr); err != nil {
		return err
	}

	// Validate against whitelist if enabled
	if err := k.validateWhitelist(ctx, genesisAddr); err != nil {
		return err
	}

	return nil
}

// validateAddresses performs basic address validation
func (k Keeper) validateAddresses(genesisAddr, recipientAddr sdk.AccAddress) error {
	if genesisAddr == nil {
		return errors.ErrInvalidAddress.Wrap("genesis address cannot be nil")
	}
	if recipientAddr == nil {
		return errors.ErrInvalidAddress.Wrap("recipient address cannot be nil")
	}
	if genesisAddr.Equals(recipientAddr) {
		return types.ErrInvalidTransfer.Wrap("cannot transfer to the same address")
	}

	// Validate address format
	if err := sdk.VerifyAddressFormat(genesisAddr); err != nil {
		return errors.ErrInvalidAddress.Wrapf("invalid genesis address format: %v", err)
	}
	if err := sdk.VerifyAddressFormat(recipientAddr); err != nil {
		return errors.ErrInvalidAddress.Wrapf("invalid recipient address format: %v", err)
	}

	return nil
}

// validateTransferHistory checks if the account has already been transferred (one-time enforcement)
func (k Keeper) validateTransferHistory(ctx context.Context, genesisAddr sdk.AccAddress) error {
	// Check if a transfer record already exists for this genesis account
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	key := genesisAddr.Bytes()
	if transferStore.Has(key) {
		// Transfer record exists, decode it to get details
		bz := transferStore.Get(key)
		var record types.TransferRecord
		if err := k.cdc.Unmarshal(bz, &record); err != nil {
			return errorsmod.Wrapf(err, "failed to decode transfer record for %s", genesisAddr.String())
		}

		return types.ErrAlreadyTransferred.Wrapf(
			"genesis account %s has already been transferred to %s at height %d",
			genesisAddr.String(),
			record.RecipientAddress,
			record.TransferHeight,
		)
	}

	return nil
}

// validateAccountExistence verifies that the genesis account exists and recipient handling
func (k Keeper) validateAccountExistence(ctx context.Context, genesisAddr, recipientAddr sdk.AccAddress) error {
	// Verify genesis account exists
	genesisAccount := k.accountKeeper.GetAccount(ctx, genesisAddr)
	if genesisAccount == nil {
		return types.ErrAccountNotFound.Wrapf("genesis account %s does not exist", genesisAddr.String())
	}

	// Recipient account existence is not required - it will be created if needed during transfer
	// But we can validate that if it exists, it's not a module account or other special account
	recipientAccount := k.accountKeeper.GetAccount(ctx, recipientAddr)
	if recipientAccount != nil {
		// Check if recipient is a module account (these shouldn't receive genesis transfers)
		if moduleAcc, ok := recipientAccount.(sdk.ModuleAccountI); ok {
			return types.ErrInvalidTransfer.Wrapf(
				"cannot transfer to module account: %s (module: %s)",
				recipientAddr.String(),
				moduleAcc.GetName(),
			)
		}
	}

	return nil
}

// validateAccountBalance ensures the genesis account has assets to transfer
func (k Keeper) validateAccountBalance(ctx context.Context, genesisAddr sdk.AccAddress) error {
	// Check liquid balances
	balances := k.bankView.GetAllBalances(ctx, genesisAddr)
	spendableCoins := k.bankView.SpendableCoins(ctx, genesisAddr)

	// Check vesting balances
	hasVesting, vestingCoins, _, err := k.GetVestingInfo(ctx, genesisAddr)
	if err != nil {
		return errorsmod.Wrapf(err, "failed to get vesting info for %s", genesisAddr.String())
	}

	// Account must have either liquid balances, spendable coins, or vesting coins
	if balances.IsZero() && spendableCoins.IsZero() && (!hasVesting || vestingCoins.IsZero()) {
		return types.ErrInvalidTransfer.Wrapf(
			"genesis account %s has no transferable assets (no liquid, spendable, or vesting balances)",
			genesisAddr.String(),
		)
	}

	// Log the validation result for debugging
	k.Logger().Info(
		"account balance validation passed",
		"genesis_address", genesisAddr.String(),
		"total_balances", balances.String(),
		"spendable_coins", spendableCoins.String(),
		"has_vesting", hasVesting,
		"vesting_coins", vestingCoins.String(),
	)

	return nil
}

// validateWhitelist checks if the account is allowed to be transferred (if whitelist is enabled)
func (k Keeper) validateWhitelist(ctx context.Context, genesisAddr sdk.AccAddress) error {
	// Get module parameters
	params := k.GetParams(ctx)

	// If whitelist restriction is disabled, allow all transfers
	if !params.RestrictToList {
		return nil
	}

	// Check if the account is in the allowed list
	if !k.IsTransferableAccount(ctx, genesisAddr.String()) {
		return types.ErrNotInAllowedList.Wrapf(
			"genesis account %s is not in the allowed accounts whitelist",
			genesisAddr.String(),
		)
	}

	return nil
}

// IsTransferableAccount checks if an account is in the whitelist of transferable accounts
func (k Keeper) IsTransferableAccount(ctx context.Context, address string) bool {
	// First validate the address format - invalid addresses are never transferable
	if address == "" {
		return false
	}

	// Validate address format
	_, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return false
	}

	// Get module parameters
	params := k.GetParams(ctx)

	// If whitelist restriction is disabled, all valid addresses are transferable
	if !params.RestrictToList {
		return true
	}

	// Check if address is in the allowed accounts list
	for _, allowedAddr := range params.AllowedAccounts {
		if allowedAddr == address {
			return true
		}
	}

	return false
}

// ValidateTransferEligibility provides a comprehensive eligibility check for UI/API queries
// Returns detailed information about transfer eligibility including specific reasons for ineligibility
func (k Keeper) ValidateTransferEligibility(ctx context.Context, genesisAddr sdk.AccAddress) (bool, string, bool, error) {
	if genesisAddr == nil {
		return false, "invalid address: genesis address cannot be nil", false, nil
	}

	// Check if already transferred
	if err := k.validateTransferHistory(ctx, genesisAddr); err != nil {
		if errorsmod.IsOf(err, types.ErrAlreadyTransferred) {
			return false, "account has already been transferred", true, nil
		}
		return false, fmt.Sprintf("failed to check transfer history: %v", err), false, err
	}

	// Check account existence
	if err := k.validateAccountExistence(ctx, genesisAddr, genesisAddr); err != nil {
		if errorsmod.IsOf(err, types.ErrAccountNotFound) {
			return false, "genesis account does not exist", false, nil
		}
		return false, fmt.Sprintf("account validation failed: %v", err), false, err
	}

	// Check account balance
	if err := k.validateAccountBalance(ctx, genesisAddr); err != nil {
		if errorsmod.IsOf(err, types.ErrInvalidTransfer) {
			return false, "account has no transferable assets", false, nil
		}
		return false, fmt.Sprintf("balance validation failed: %v", err), false, err
	}

	// Check whitelist
	if err := k.validateWhitelist(ctx, genesisAddr); err != nil {
		if errorsmod.IsOf(err, types.ErrNotInAllowedList) {
			return false, "account is not in the allowed accounts whitelist", false, nil
		}
		return false, fmt.Sprintf("whitelist validation failed: %v", err), false, err
	}

	// All validations passed
	return true, "account is eligible for transfer", false, nil
}

// GetTransferRecord retrieves a transfer record for a genesis account
func (k Keeper) GetTransferRecord(ctx context.Context, genesisAddr sdk.AccAddress) (*types.TransferRecord, bool, error) {
	if genesisAddr == nil {
		return nil, false, errors.ErrInvalidAddress.Wrap("genesis address cannot be nil")
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	key := genesisAddr.Bytes()
	if !transferStore.Has(key) {
		return nil, false, nil
	}

	bz := transferStore.Get(key)
	var record types.TransferRecord
	if err := k.cdc.Unmarshal(bz, &record); err != nil {
		return nil, false, errorsmod.Wrapf(err, "failed to decode transfer record for %s", genesisAddr.String())
	}

	return &record, true, nil
}

// SetTransferRecord stores a transfer record for a genesis account
func (k Keeper) SetTransferRecord(ctx context.Context, record types.TransferRecord) error {
	// Validate the record
	if err := record.Validate(); err != nil {
		return errorsmod.Wrapf(err, "invalid transfer record")
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	// Parse genesis address for the key
	genesisAddr, err := sdk.AccAddressFromBech32(record.GenesisAddress)
	if err != nil {
		return errorsmod.Wrapf(err, "invalid genesis address in record: %s", record.GenesisAddress)
	}

	key := genesisAddr.Bytes()
	bz, err := k.cdc.Marshal(&record)
	if err != nil {
		return errorsmod.Wrapf(err, "failed to marshal transfer record")
	}

	transferStore.Set(key, bz)

	// Log the record storage
	k.Logger().Info(
		"transfer record stored",
		"genesis_address", record.GenesisAddress,
		"recipient_address", record.RecipientAddress,
		"transfer_height", record.TransferHeight,
	)

	return nil
}
