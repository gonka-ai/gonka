package apiconfig

import (
	"context"
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

func NewApiAccount(ctx context.Context, addressPrefix string, nodeConfig ChainNodeConfig) (*ApiAccount, error) {
	client, err := cosmosclient.New(
		ctx,
		cosmosclient.WithAddressPrefix(addressPrefix),
		cosmosclient.WithKeyringBackend(cosmosaccount.KeyringBackend(nodeConfig.KeyringBackend)),
		cosmosclient.WithKeyringDir(nodeConfig.KeyringDir),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cosmos client: %w", err)
	}

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

// AccountAddress returns the bech32 address of the main account (cold wallet).
func (a *ApiAccount) AccountAddress() string {
	// A PubKey's address is deterministic, so this cannot fail.
	return types.AccAddress(a.AccountKey.Address()).String()
}

// SignerAddress returns the bech32 address of the signer account (hot wallet).
func (a *ApiAccount) SignerAddress() (types.AccAddress, error) {
	address, err := a.SignerAccount.Address(a.AddressPrefix)
	if err != nil {
		return types.AccAddress{}, fmt.Errorf("could not get signer address: %w", err)
	}
	return types.AccAddress(address), nil
}

// IsSignerTheMainAccount checks if the signer key is the same as the main account key.
func (a *ApiAccount) IsSignerTheMainAccount() bool {
	return a.SignerAccount.Record.PubKey.Equal(a.AccountKey)
}
