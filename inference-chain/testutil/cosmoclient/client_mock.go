package testutil

import (
	"context"
	"github.com/cometbft/cometbft/p2p"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient/mocks"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"testing"
)

type MockClient struct {
	context client.Context
}

func NewMockClient(t *testing.T, rpc *mocks.RPCClient, network, accountName, mnemonic, pass string) cosmosclient.Client {
	ctx := context.TODO()
	rpc.EXPECT().Status(ctx).Return(&ctypes.ResultStatus{
		NodeInfo: p2p.DefaultNodeInfo{
			Network: network,
		},
	}, nil)

	tmpDir := t.TempDir()
	client, err := cosmosclient.New(
		ctx,
		cosmosclient.WithRPCClient(rpc),
		cosmosclient.WithBankQueryClient(mocks.NewBankQueryClient(t)),
		cosmosclient.WithAccountRetriever(mocks.NewAccountRetriever(t)),
		cosmosclient.WithKeyringDir(tmpDir),
		cosmosclient.WithKeyringServiceName(accountName),
	)

	types.RegisterInterfaces(client.Context().InterfaceRegistry)
	assert.NoError(t, err)
	_, err = client.AccountRegistry.Import(accountName, mnemonic, pass)
	assert.NoError(t, err)
	return client
}

func (m *MockClient) Context() client.Context {
	return m.context
}
