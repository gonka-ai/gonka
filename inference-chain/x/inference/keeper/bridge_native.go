package keeper

import (
	"context"
	"fmt"
	"strings"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// Bridge native token operations for WGNK bridging

// GetBridgeEscrowAddress returns the bridge escrow module account address
func (k Keeper) GetBridgeEscrowAddress() sdk.AccAddress {
	return k.AccountKeeper.GetModuleAddress(types.BridgeEscrowAccName)
}

// GetBridgeEscrowBalance returns the current balance of native tokens in the bridge escrow account
func (k Keeper) GetBridgeEscrowBalance(ctx sdk.Context, denom string) sdk.Coin {
	escrowAddr := k.GetBridgeEscrowAddress()
	return k.BankView.SpendableCoin(ctx, escrowAddr, denom)
}

// TransferToEscrow transfers native tokens from user to bridge escrow account
func (k Keeper) TransferToEscrow(ctx sdk.Context, fromAddr sdk.AccAddress, amount sdk.Coins) error {
	// Use SendCoinsFromAccountToModule for proper module account handling
	return k.BankKeeper.SendCoinsFromAccountToModule(ctx, fromAddr, types.BridgeEscrowAccName, amount, "bridge_escrow")
}

// ReleaseFromEscrow transfers native tokens from bridge escrow account to user
func (k Keeper) ReleaseFromEscrow(ctx sdk.Context, toAddr sdk.AccAddress, amount sdk.Coins) error {
	// Use SendCoinsFromModuleToAccount for proper module account handling
	return k.BankKeeper.SendCoinsFromModuleToAccount(ctx, types.BridgeEscrowAccName, toAddr, amount, "bridge_release")
}

// IsBridgeContractAddress checks if the given contract address matches any registered bridge addresses
func (k Keeper) IsBridgeContractAddress(ctx context.Context, contractAddress string) (bool, string) {
	// Get all registered bridge contract addresses
	allBridgeAddresses := k.GetAllBridgeContractAddresses(ctx)

	// Normalize the input address for comparison
	normalizedInput := strings.ToLower(contractAddress)

	for _, bridgeAddr := range allBridgeAddresses {
		if strings.ToLower(bridgeAddr.Address) == normalizedInput {
			return true, bridgeAddr.ChainId
		}
	}

	return false, ""
}

// HandleNativeTokenRelease handles the release of native tokens when WGNK is burned on Ethereum
func (k Keeper) HandleNativeTokenRelease(ctx sdk.Context, bridgeTx *types.BridgeTransaction) error {
	// Parse the recipient address (should be a valid Cosmos address)
	recipientAddr, err := sdk.AccAddressFromBech32(bridgeTx.OwnerAddress)
	if err != nil {
		return fmt.Errorf("invalid recipient address %s: %v", bridgeTx.OwnerAddress, err)
	}

	// Parse the amount
	amountInt, ok := math.NewIntFromString(bridgeTx.Amount)
	if !ok {
		return fmt.Errorf("invalid amount %s", bridgeTx.Amount)
	}

	// Create coins for the native token using the chain's base denom
	// TODO: Make this configurable or derive from chain parameters/metadata
	nativeCoins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, amountInt))

	// Check if escrow has sufficient balance
	escrowBalance := k.GetBridgeEscrowBalance(ctx, types.BaseCoin)
	if escrowBalance.Amount.LT(amountInt) {
		return fmt.Errorf("insufficient escrow balance: have %s, need %s", escrowBalance.Amount.String(), amountInt.String())
	}

	// Release tokens from escrow to recipient
	err = k.ReleaseFromEscrow(ctx, recipientAddr, nativeCoins)
	if err != nil {
		return fmt.Errorf("failed to release tokens from escrow: %v", err)
	}

	k.LogInfo("Bridge native: Successfully released native tokens from escrow",
		types.Messages,
		"recipient", bridgeTx.OwnerAddress,
		"amount", bridgeTx.Amount,
		"chainId", bridgeTx.ChainId,
		"contractAddress", bridgeTx.ContractAddress)

	return nil
}

// ValidateBridgeMintRequest validates the parameters for a bridge mint request
func (k Keeper) ValidateBridgeMintRequest(ctx sdk.Context, creator sdk.AccAddress, amount string, chainId string) error {
	// Parse amount
	amountInt, ok := math.NewIntFromString(amount)
	if !ok {
		return fmt.Errorf("invalid amount format: %s", amount)
	}

	if amountInt.IsZero() || amountInt.IsNegative() {
		return fmt.Errorf("amount must be positive: %s", amount)
	}

	// Check user has sufficient balance
	userBalance := k.BankView.SpendableCoin(ctx, creator, types.BaseCoin)

	if userBalance.Amount.LT(amountInt) {
		return fmt.Errorf("insufficient balance: have %s, need %s", userBalance.Amount.String(), amountInt.String())
	}

	// Validate chain ID is supported and has registered bridge addresses
	bridgeAddresses := k.GetBridgeContractAddressesByChain(ctx, chainId)
	if len(bridgeAddresses) == 0 {
		return fmt.Errorf("no bridge addresses registered for chain: %s", chainId)
	}

	return nil
}
