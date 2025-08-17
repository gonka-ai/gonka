package keeper

import (
	"context"
	"sort"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/genesistransfer/types"
)

// WhitelistManager provides comprehensive whitelist management functionality
// This provides a centralized interface for all whitelist operations

// IsWhitelistEnabled returns whether whitelist enforcement is currently enabled
func (k Keeper) IsWhitelistEnabled(ctx context.Context) bool {
	return k.GetRestrictToList(ctx)
}

// EnableWhitelist enables whitelist enforcement
func (k Keeper) EnableWhitelist(ctx context.Context) error {
	return k.SetRestrictToList(ctx, true)
}

// DisableWhitelist disables whitelist enforcement
func (k Keeper) DisableWhitelist(ctx context.Context) error {
	return k.SetRestrictToList(ctx, false)
}

// GetWhitelistSize returns the number of accounts in the whitelist
func (k Keeper) GetWhitelistSize(ctx context.Context) int {
	allowedAccounts := k.GetAllowedAccounts(ctx)
	return len(allowedAccounts)
}

// IsAccountWhitelisted checks if a specific account is in the whitelist
// This is an alias for IsTransferableAccount when whitelist is enabled
func (k Keeper) IsAccountWhitelisted(ctx context.Context, address string) bool {
	if !k.IsWhitelistEnabled(ctx) {
		return true // If whitelist is disabled, all accounts are considered "whitelisted"
	}

	allowedAccounts := k.GetAllowedAccounts(ctx)
	for _, addr := range allowedAccounts {
		if addr == address {
			return true
		}
	}
	return false
}

// AddAccountsToWhitelist adds multiple accounts to the whitelist in a single operation
func (k Keeper) AddAccountsToWhitelist(ctx context.Context, addresses []string) error {
	if len(addresses) == 0 {
		return nil // No addresses to add
	}

	// Validate all addresses first
	for _, addr := range addresses {
		if _, err := sdk.AccAddressFromBech32(addr); err != nil {
			return errorsmod.Wrapf(err, "invalid address: %s", addr)
		}
	}

	params := k.GetParams(ctx)

	// Create a map of existing addresses for efficient lookup
	existing := make(map[string]bool)
	for _, addr := range params.AllowedAccounts {
		existing[addr] = true
	}

	// Add new addresses (avoiding duplicates)
	var newAddresses []string
	for _, addr := range addresses {
		if !existing[addr] {
			params.AllowedAccounts = append(params.AllowedAccounts, addr)
			newAddresses = append(newAddresses, addr)
		}
	}

	if len(newAddresses) == 0 {
		return errorsmod.Wrapf(types.ErrInvalidTransfer, "all provided addresses are already in the whitelist")
	}

	// Sort the list for consistent ordering
	sort.Strings(params.AllowedAccounts)

	err := k.SetParams(ctx, params)
	if err != nil {
		return err
	}

	k.Logger().Info(
		"accounts added to whitelist",
		"count", len(newAddresses),
		"addresses", newAddresses,
	)

	return nil
}

// RemoveAccountsFromWhitelist removes multiple accounts from the whitelist in a single operation
func (k Keeper) RemoveAccountsFromWhitelist(ctx context.Context, addresses []string) error {
	if len(addresses) == 0 {
		return nil // No addresses to remove
	}

	params := k.GetParams(ctx)

	// Create a map of addresses to remove for efficient lookup
	toRemove := make(map[string]bool)
	for _, addr := range addresses {
		toRemove[addr] = true
	}

	// Filter out addresses to remove
	var newAllowedAccounts []string
	var removedAddresses []string

	for _, addr := range params.AllowedAccounts {
		if toRemove[addr] {
			removedAddresses = append(removedAddresses, addr)
		} else {
			newAllowedAccounts = append(newAllowedAccounts, addr)
		}
	}

	if len(removedAddresses) == 0 {
		return errorsmod.Wrapf(types.ErrAccountNotFound, "none of the provided addresses were found in the whitelist")
	}

	params.AllowedAccounts = newAllowedAccounts
	err := k.SetParams(ctx, params)
	if err != nil {
		return err
	}

	k.Logger().Info(
		"accounts removed from whitelist",
		"count", len(removedAddresses),
		"addresses", removedAddresses,
	)

	return nil
}

// ClearWhitelist removes all accounts from the whitelist
func (k Keeper) ClearWhitelist(ctx context.Context) error {
	params := k.GetParams(ctx)
	originalCount := len(params.AllowedAccounts)

	if originalCount == 0 {
		return nil // Already empty
	}

	params.AllowedAccounts = []string{}
	err := k.SetParams(ctx, params)
	if err != nil {
		return err
	}

	k.Logger().Info(
		"whitelist cleared",
		"removed_count", originalCount,
	)

	return nil
}

// GetWhitelistStats returns comprehensive statistics about the whitelist
func (k Keeper) GetWhitelistStats(ctx context.Context) WhitelistStats {
	return WhitelistStats{
		Enabled:      k.IsWhitelistEnabled(ctx),
		AccountCount: k.GetWhitelistSize(ctx),
		Accounts:     k.GetAllowedAccounts(ctx),
	}
}

// WhitelistStats contains statistics about the current whitelist configuration
type WhitelistStats struct {
	Enabled      bool     `json:"enabled"`
	AccountCount int      `json:"account_count"`
	Accounts     []string `json:"accounts"`
}

// ValidateWhitelistConfiguration validates the current whitelist configuration
func (k Keeper) ValidateWhitelistConfiguration(ctx context.Context) error {
	params := k.GetParams(ctx)

	// If whitelist is enabled but empty, this might be an issue
	if params.RestrictToList && len(params.AllowedAccounts) == 0 {
		k.Logger().Warn(
			"whitelist enforcement is enabled but no accounts are whitelisted - all transfers will be rejected",
		)
		// This is not an error, but a warning condition
	}

	// Validate all addresses in the whitelist
	for i, addr := range params.AllowedAccounts {
		if _, err := sdk.AccAddressFromBech32(addr); err != nil {
			return errorsmod.Wrapf(err, "invalid address at index %d: %s", i, addr)
		}
	}

	return nil
}

// GetWhitelistDifference compares two address lists and returns the differences
func (k Keeper) GetWhitelistDifference(current, proposed []string) (toAdd, toRemove []string) {
	currentMap := make(map[string]bool)
	for _, addr := range current {
		currentMap[addr] = true
	}

	proposedMap := make(map[string]bool)
	for _, addr := range proposed {
		proposedMap[addr] = true
	}

	// Find addresses to add (in proposed but not in current)
	for _, addr := range proposed {
		if !currentMap[addr] {
			toAdd = append(toAdd, addr)
		}
	}

	// Find addresses to remove (in current but not in proposed)
	for _, addr := range current {
		if !proposedMap[addr] {
			toRemove = append(toRemove, addr)
		}
	}

	return toAdd, toRemove
}

// UpdateWhitelist replaces the entire whitelist with a new list of accounts
func (k Keeper) UpdateWhitelist(ctx context.Context, newAccounts []string) error {
	// Validate all new addresses
	for i, addr := range newAccounts {
		if _, err := sdk.AccAddressFromBech32(addr); err != nil {
			return errorsmod.Wrapf(err, "invalid address at index %d: %s", i, addr)
		}
	}

	// Remove duplicates and sort
	uniqueAccounts := make(map[string]bool)
	var cleanedAccounts []string

	for _, addr := range newAccounts {
		if !uniqueAccounts[addr] {
			uniqueAccounts[addr] = true
			cleanedAccounts = append(cleanedAccounts, addr)
		}
	}

	sort.Strings(cleanedAccounts)

	params := k.GetParams(ctx)
	oldCount := len(params.AllowedAccounts)
	params.AllowedAccounts = cleanedAccounts

	err := k.SetParams(ctx, params)
	if err != nil {
		return err
	}

	k.Logger().Info(
		"whitelist updated",
		"old_count", oldCount,
		"new_count", len(cleanedAccounts),
	)

	return nil
}
