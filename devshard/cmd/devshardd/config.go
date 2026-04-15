package main

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
)

// ChainNodeConfig holds the chain connectivity and signing identity settings
// for devshardd. Inlined from decentralized-api/apiconfig to remove that
// dependency.
type ChainNodeConfig struct {
	Url             string // Tendermint RPC URL (e.g. http://node:26657)
	GrpcUrl         string // gRPC URL for chain.Client (e.g. node:9090)
	IsGenesis       bool
	SignerKeyName   string
	KeyringBackend  string
	KeyringDir      string
	KeyringPassword string
}

// ApiAccount holds the account and signer keys for devshardd.
// Inlined from decentralized-api/apiconfig to remove that dependency.
// Only the fields and methods actually used by devshardd are included.
type ApiAccount struct {
	AccountKey    cryptotypes.PubKey
	SignerRecord  *keyring.Record
	AddressPrefix string
}

// AccountAddressBech32 returns the bech32-encoded account address.
func (a *ApiAccount) AccountAddressBech32() (string, error) {
	addr, err := sdktypes.Bech32ifyAddressBytes(a.AddressPrefix, a.AccountKey.Address())
	if err != nil {
		return "", fmt.Errorf("failed to Bech32-encode address: %w", err)
	}
	return addr, nil
}

// SignerAddressBech32 returns the bech32-encoded signer address.
func (a *ApiAccount) SignerAddressBech32() (string, error) {
	pubKey, err := a.SignerRecord.GetPubKey()
	if err != nil {
		return "", fmt.Errorf("failed to get signer public key: %w", err)
	}
	addr, err := sdktypes.Bech32ifyAddressBytes(a.AddressPrefix, pubKey.Address())
	if err != nil {
		return "", fmt.Errorf("failed to Bech32-encode address: %w", err)
	}
	return addr, nil
}
