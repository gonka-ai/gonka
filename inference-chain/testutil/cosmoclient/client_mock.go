package testutil

import (
	context2 "context"
	"github.com/cometbft/cometbft/p2p"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient/mocks"
	"github.com/stretchr/testify/assert"
	"testing"
)

type MockClient struct {
	context client.Context
}

func NewMockClient(t *testing.T, network, accountName, mnemonic, pass string) cosmosclient.Client {
	ctx := context2.TODO()
	rpc := mocks.NewRPCClient(t)
	rpc.EXPECT().Status(ctx).Return(&ctypes.ResultStatus{
		NodeInfo: p2p.DefaultNodeInfo{
			Network: network,
		},
	}, nil)

	client, err := cosmosclient.New(
		ctx,
		cosmosclient.WithRPCClient(rpc),
		cosmosclient.WithBankQueryClient(mocks.NewBankQueryClient(t)),
		cosmosclient.WithAccountRetriever(mocks.NewAccountRetriever(t)),
	)
	assert.NoError(t, err)
	_, err = client.AccountRegistry.Import(accountName, mnemonic, pass)
	assert.NoError(t, err)
	return client
}

func (m *MockClient) Context() client.Context {
	return m.context
}
