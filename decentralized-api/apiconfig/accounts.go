package apiconfig

import (
	"bytes"
	"encoding/base64"
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosaccount"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
)

type ApiAccount struct {
	AccountKey    cryptotypes.PubKey
	SignerAccount *cosmosaccount.Account
	AddressPrefix string
}

func NewApiAccount(addressPrefix string, nodeConfig ChainNodeConfig, client *cosmosclient.Client) (*ApiAccount, error) {
	signer, err := client.AccountRegistry.GetByName(nodeConfig.SignerKeyName)
	if err != nil {
		return nil, fmt.Errorf("failed to get signer account '%s' from keyring: %w", nodeConfig.SignerKeyName, err)
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(nodeConfig.AccountPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode account public key: %w", err)
	}
	accountKey := secp256k1.PubKey{Key: pubKeyBytes}
	return &ApiAccount{
		AccountKey:    &accountKey,
		SignerAccount: &signer,
		AddressPrefix: addressPrefix,
	}, nil
}

func (a *ApiAccount) AccountAddressBech32() (string, error) {
	addr, err := types.Bech32ifyAddressBytes(a.AddressPrefix, a.AccountKey.Address())
	if err != nil {
		return "", fmt.Errorf("failed to Bech32-encode address: %w", err)
	}
	return addr, nil
}

func (a *ApiAccount) AccountAddress() (types.AccAddress, error) {
	return types.AccAddress(a.AccountKey.Address()), nil
}

func (a *ApiAccount) SignerAddressBech32() (string, error) {
	pubKey, err := a.SignerAccount.Record.GetPubKey()
	if err != nil {
		return "", fmt.Errorf("failed to get signer public key: %w", err)
	}
	addr, err := types.Bech32ifyAddressBytes(a.AddressPrefix, pubKey.Address())
	if err != nil {
		return "", fmt.Errorf("failed to Bech32-encode address: %w", err)
	}
	return addr, nil
}

func (a *ApiAccount) SignerAddress() (types.AccAddress, error) {
	pubKey, err := a.SignerAccount.Record.GetPubKey()
	if err != nil {
		return types.AccAddress{}, fmt.Errorf("failed to get signer public key: %w", err)
	}
	return types.AccAddress(pubKey.Address()), nil
}

func (a *ApiAccount) IsSignerTheMainAccount() bool {
	signerPubKey, err := a.SignerAccount.Record.GetPubKey()
	if err != nil {
		return false
	}

	return bytes.Equal(signerPubKey.Bytes(), a.AccountKey.Bytes())
}
