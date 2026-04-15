package main

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	"common/chain"
	"devshard/cmd/devshardd/inference"
	"devshard/cmd/devshardd/session"
)

// chainIdentity wraps a common/chain.Client with signing identity metadata.
// It satisfies both session.PayloadAuthClient and inference.PayloadAuthClient
// without depending on the ignite client for gRPC connectivity.
type chainIdentity struct {
	client      *chain.Client
	accountAddr string
	signerAddr  string
	keyring     *keyring.Keyring
}

func newChainIdentity(
	client *chain.Client,
	apiAccount ApiAccount,
	kr keyring.Keyring,
) (*chainIdentity, error) {
	accountAddr, err := apiAccount.AccountAddressBech32()
	if err != nil {
		return nil, fmt.Errorf("account address: %w", err)
	}
	signerAddr, err := apiAccount.SignerAddressBech32()
	if err != nil {
		return nil, fmt.Errorf("signer address: %w", err)
	}
	return &chainIdentity{
		client:      client,
		accountAddr: accountAddr,
		signerAddr:  signerAddr,
		keyring:     &kr,
	}, nil
}

func (c *chainIdentity) NewInferenceQueryClient() inferencetypes.QueryClient {
	return inferencetypes.NewQueryClient(c.client.Conn())
}

func (c *chainIdentity) GetAccountAddress() string {
	return c.accountAddr
}

func (c *chainIdentity) GetSignerAddress() string {
	return c.signerAddr
}

func (c *chainIdentity) GetKeyring() *keyring.Keyring {
	return c.keyring
}

var _ session.PayloadAuthClient = (*chainIdentity)(nil)
var _ inference.PayloadAuthClient = (*chainIdentity)(nil)
