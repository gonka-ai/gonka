package app

import sdk "github.com/cosmos/cosmos-sdk/types"

// CoinType is the SLIP-44 coin type used for HD key derivation on the Gonka chain.
const CoinType = 1200

// InitSDKConfig sets the chain's bech32 prefixes and coin type on the
// global sdk.Config. Idempotent. Does not seal; production callers must
// call SealSDKConfig after this.
//
// Tests call this without sealing so per-test helpers like createTestApp
// can re-set the same prefixes from package init scope under the sims
// build tag without hitting the sealed-config panic.
func InitSDKConfig() {
	accountPubKeyPrefix := AccountAddressPrefix + "pub"
	validatorAddressPrefix := AccountAddressPrefix + "valoper"
	validatorPubKeyPrefix := AccountAddressPrefix + "valoperpub"
	consNodeAddressPrefix := AccountAddressPrefix + "valcons"
	consNodePubKeyPrefix := AccountAddressPrefix + "valconspub"

	config := sdk.GetConfig()
	config.SetCoinType(CoinType) // TODO: change to custom coin type
	config.SetBech32PrefixForAccount(AccountAddressPrefix, accountPubKeyPrefix)
	config.SetBech32PrefixForValidator(validatorAddressPrefix, validatorPubKeyPrefix)
	config.SetBech32PrefixForConsensusNode(consNodeAddressPrefix, consNodePubKeyPrefix)
}

// SealSDKConfig seals the global sdk.Config so subsequent SetBech32 or
// SetCoinType calls panic. Production callers (cmd/inferenced) call this
// after InitSDKConfig at startup so address parsing stays strict for
// the lifetime of the process. Tests do not call it; sealing breaks
// createTestApp and other helpers that re-set prefixes per test.
func SealSDKConfig() {
	sdk.GetConfig().Seal()
}
