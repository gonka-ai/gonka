package keeper

import (
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/productscience/inference/x/restrictions/types"
)

// SendRestrictionFn implements the SendRestriction function for the bank module
// This function is called before every coin transfer to validate if it should be allowed
func (k Keeper) SendRestrictionFn(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
	// Convert context to SDK context for our internal operations
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	// Check if restrictions are active
	if !k.IsRestrictionActive(sdkCtx) {
		// Restrictions are not active, allow all transfers
		return to, nil
	}

	// 1. PERMITTED - Gas Fee Payments
	if k.IsGasFeePayment(to) {
		return to, nil
	}

	// 2. PERMITTED - User-to-Module Transfers
	// Any transfer from user to module account is allowed (inference escrow, governance deposits, etc.)
	if k.IsModuleAccount(to) {
		return to, nil
	}

	// 3. PERMITTED - Module Operations
	// Any transfer from module account to any account is allowed (rewards, refunds, etc.)
	if k.IsModuleAccount(from) {
		return to, nil
	}

	// 4. PERMITTED - Emergency Exemption Transfers
	if k.MatchesEmergencyExemption(sdkCtx, from, to, amt) {
		return to, nil
	}

	// 5. RESTRICTED - Direct User Transfers
	// This is a user-to-user transfer, which is restricted
	params := k.GetParams(sdkCtx)
	remainingBlocks := params.RestrictionEndBlock - uint64(sdkCtx.BlockHeight())

	return to, errorsmod.Wrapf(
		types.ErrTransferRestricted,
		"user-to-user transfers are restricted during bootstrap period. Restriction ends at block %d (current: %d, remaining: %d blocks). Allowed transfers: gas payments, protocol interactions (inference, governance, staking), and module operations",
		params.RestrictionEndBlock,
		sdkCtx.BlockHeight(),
		remainingBlocks,
	)
}

// IsRestrictionActive checks if transfer restrictions are currently active
func (k Keeper) IsRestrictionActive(ctx sdk.Context) bool {
	params := k.GetParams(ctx)
	currentHeight := uint64(ctx.BlockHeight())
	return currentHeight < params.RestrictionEndBlock
}

// IsGasFeePayment checks if the transfer is a gas fee payment to the fee collector
func (k Keeper) IsGasFeePayment(toAddr sdk.AccAddress) bool {
	feeCollectorAddr := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	return toAddr.Equals(feeCollectorAddr)
}

// IsModuleAccount checks if the given address is a module account
func (k Keeper) IsModuleAccount(addr sdk.AccAddress) bool {
	// Check if it's a known module account by trying to get the module name
	// This is a simple check - in practice, you might want to maintain a registry
	// or use the account keeper to check if it's a module account

	// Common module accounts we know about
	knownModules := []string{
		authtypes.FeeCollectorName,
		"inference",
		"streamvesting",
		"collateral",
		"bookkeeper",
		"gov",
		"distribution",
		"bonded_tokens_pool",
		"not_bonded_tokens_pool",
		"mint",
		"bls",
	}

	for _, moduleName := range knownModules {
		moduleAddr := authtypes.NewModuleAddress(moduleName)
		if addr.Equals(moduleAddr) {
			return true
		}
	}

	// Additional check: if the address has the module account prefix
	// Module accounts typically have addresses that start with specific patterns
	// This is a heuristic check
	addrStr := addr.String()
	if len(addrStr) >= 20 && (addrStr[:4] == "cosm" || addrStr[:7] == "cosmos1") {
		// Check if it looks like a module account (typically longer and with specific patterns)
		// This is not foolproof but helps catch most module accounts
		if len(addrStr) > 50 {
			return true
		}
	}

	return false
}

// MatchesEmergencyExemption checks if a transfer matches any active emergency exemption
func (k Keeper) MatchesEmergencyExemption(ctx sdk.Context, from, to sdk.AccAddress, amt sdk.Coins) bool {
	params := k.GetParams(ctx)
	currentHeight := uint64(ctx.BlockHeight())

	// Check each exemption
	for _, exemption := range params.EmergencyTransferExemptions {
		// Check if exemption is still active
		if exemption.ExpiryBlock <= currentHeight {
			continue
		}

		// Check address matching
		fromStr := from.String()
		toStr := to.String()

		// Check from address (wildcard "*" means any address)
		if exemption.FromAddress != "*" && exemption.FromAddress != fromStr {
			continue
		}

		// Check to address (wildcard "*" means any address)
		if exemption.ToAddress != "*" && exemption.ToAddress != toStr {
			continue
		}

		// Check amount limits for each coin in the transfer
		maxAmount, err := strconv.ParseUint(exemption.MaxAmount, 10, 64)
		if err != nil {
			// Invalid exemption amount, skip
			continue
		}

		// Check if all coins in the transfer are within the limit
		totalAmount := uint64(0)
		for _, coin := range amt {
			// For now, we'll sum all coin amounts regardless of denom
			// In practice, you might want to check per denomination
			totalAmount += coin.Amount.Uint64()
		}

		if totalAmount > maxAmount {
			continue
		}

		// Check usage limits
		currentUsage := uint64(0)
		for _, usage := range params.ExemptionUsageTracking {
			if usage.ExemptionId == exemption.ExemptionId && usage.AccountAddress == fromStr {
				currentUsage = usage.UsageCount
				break
			}
		}

		if currentUsage >= exemption.UsageLimit {
			continue
		}

		// This exemption matches and has usage remaining
		return true
	}

	return false
}

// GetTransferRestrictionFunction returns the transfer restriction function for bank module integration
func (k Keeper) GetTransferRestrictionFunction() func(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
	return k.SendRestrictionFn
}
