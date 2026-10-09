package main

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	"common/chain"
	"devshard/cmd/devshardd/inference"
	"devshard/cmd/devshardd/session"
	"devshard/signing"
)

// chainIdentity wraps a common/chain.Client with signing identity metadata.
// It satisfies both session.PayloadAuthClient and inference.PayloadAuthClient
// without depending on the ignite client for gRPC connectivity.
type chainIdentity struct {
	client      *chain.Client
	accountAddr string
	signerAddr  string
	payload     *signing.CachedCosmosSigner
}

func newChainIdentity(
	client *chain.Client,
	apiAccount ApiAccount,
	payload *signing.CachedCosmosSigner,
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
		payload:     payload,
	}, nil
}

func (c *chainIdentity) NewInferenceQueryClient() inferencetypes.QueryClient {
	return inferencetypes.NewQueryClient(c.client.QueryConn())
}

func (c *chainIdentity) GetAccountAddress() string {
	return c.accountAddr
}

func (c *chainIdentity) GetSignerAddress() string {
	return c.signerAddr
}

func (c *chainIdentity) SignBytes(data []byte) (string, error) {
	if c.payload == nil {
		return "", fmt.Errorf("payload signer is not configured")
	}
	return c.payload.SignBytes(data)
}

func resolveChainID(ctx context.Context, chainClient *chain.Client, configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}

	resp, err := chainClient.CometServiceClient().GetNodeInfo(ctx, &cmtservice.GetNodeInfoRequest{})
	if err != nil {
		return "", fmt.Errorf("query node info: %w", err)
	}
	if resp == nil || resp.DefaultNodeInfo == nil || resp.DefaultNodeInfo.Network == "" {
		return "", fmt.Errorf("empty chain id from node info")
	}

	return resp.DefaultNodeInfo.Network, nil
}

var _ session.PayloadAuthClient = (*chainIdentity)(nil)
var _ inference.PayloadAuthClient = (*chainIdentity)(nil)
