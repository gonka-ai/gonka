package main

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	igniteclient "github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	"decentralized-api/apiconfig"
	internaldevshard "decentralized-api/internal/devshard"
)

// queryOnlyCosmosClient is the narrow chain dependency devshardd needs:
// inference queries plus signing identity metadata. It intentionally omits all
// transaction-sending methods because devshardd never writes to mainnet.
type queryOnlyCosmosClient struct {
	ctx         context.Context
	ignite      *igniteclient.Client
	accountAddr string
	signerAddr  string
}

func newQueryOnlyCosmosClient(
	ctx context.Context,
	ignite *igniteclient.Client,
	apiAccount apiconfig.ApiAccount,
) (*queryOnlyCosmosClient, error) {
	accountAddr, err := apiAccount.AccountAddressBech32()
	if err != nil {
		return nil, fmt.Errorf("account address: %w", err)
	}
	signerAddr, err := apiAccount.SignerAddressBech32()
	if err != nil {
		return nil, fmt.Errorf("signer address: %w", err)
	}
	return &queryOnlyCosmosClient{
		ctx:         ctx,
		ignite:      ignite,
		accountAddr: accountAddr,
		signerAddr:  signerAddr,
	}, nil
}

func (c *queryOnlyCosmosClient) NewInferenceQueryClient() inferencetypes.QueryClient {
	return inferencetypes.NewQueryClient(c.ignite.Context())
}

func (c *queryOnlyCosmosClient) GetAccountAddress() string {
	return c.accountAddr
}

func (c *queryOnlyCosmosClient) GetSignerAddress() string {
	return c.signerAddr
}

func (c *queryOnlyCosmosClient) GetKeyring() *keyring.Keyring {
	kr := c.ignite.AccountRegistry.Keyring
	return &kr
}

var _ internaldevshard.PayloadAuthClient = (*queryOnlyCosmosClient)(nil)
var _ internaldevshard.InferenceQueryClientProvider = (*queryOnlyCosmosClient)(nil)
