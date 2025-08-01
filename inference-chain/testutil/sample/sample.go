package sample

import (
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// AccAddress returns a sample account address
func AccAddress() string {
	pk := ed25519.GenPrivKey().PubKey()
	addr := pk.Address()
	return sdk.AccAddress(addr).String()
	// TODO: Check if we should use secp256k1 instead
	// return sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address()).String()
}

// AccAddressAndValAddress returns a sample account address and its corresponding validator address
func AccAddressAndValAddress() (sdk.ValAddress, sdk.AccAddress) {
	addr := secp256k1.GenPrivKey().PubKey().Address()
	return sdk.ValAddress(addr), sdk.AccAddress(addr)
}
