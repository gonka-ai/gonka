package seed

import (
	"context"
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type deterministicSeedSigner struct {
	cosmosclient.MockCosmosMessageClient
	privateKey *secp256k1.PrivKey
}

func (s *deterministicSeedSigner) SignBytes(message []byte) ([]byte, error) {
	return s.privateKey.Sign(message)
}

func TestCreateNewSeedIsDeterministicForEpoch(t *testing.T) {
	signer := &deterministicSeedSigner{privateKey: secp256k1.GenPrivKey()}
	manager := NewRandomSeedManager(signer, &apiconfig.ConfigManager{})

	first, err := manager.CreateNewSeed(344)
	require.NoError(t, err)
	second, err := manager.CreateNewSeed(344)
	require.NoError(t, err)

	require.Equal(t, first.Seed, second.Seed)
	require.Equal(t, first.Signature, second.Signature)
}

type settleQueryClient struct {
	types.QueryClient
	resp *types.QueryGetSettleAmountResponse
	err  error
}

func (q *settleQueryClient) SettleAmount(context.Context, *types.QueryGetSettleAmountRequest, ...grpc.CallOption) (*types.QueryGetSettleAmountResponse, error) {
	return q.resp, q.err
}

func TestRequestMoneyClaimsOnlyWithSettleForEpoch(t *testing.T) {
	const epoch = 416
	cases := []struct {
		name   string
		query  *settleQueryClient
		claims bool
	}{
		{"no settle", &settleQueryClient{err: status.Error(codes.NotFound, "not found")}, false},
		{"settle of another epoch", &settleQueryClient{resp: &types.QueryGetSettleAmountResponse{SettleAmount: types.SettleAmount{EpochIndex: epoch - 1}}}, false},
		{"settle of this epoch", &settleQueryClient{resp: &types.QueryGetSettleAmountResponse{SettleAmount: types.SettleAmount{EpochIndex: epoch}}}, true},
		{"query unavailable", &settleQueryClient{err: status.Error(codes.Unavailable, "down")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signer := &deterministicSeedSigner{privateKey: secp256k1.GenPrivKey()}
			signer.On("GetContext").Return(context.Background())
			signer.On("GetAddress").Return("gonka1participant")
			signer.On("NewInferenceQueryClient").Return(tc.query)
			signer.On("ClaimRewards", mock.Anything).Return(nil)

			NewRandomSeedManager(signer, &apiconfig.ConfigManager{}).RequestMoney(epoch)

			if tc.claims {
				signer.AssertCalled(t, "ClaimRewards", mock.Anything)
			} else {
				signer.AssertNotCalled(t, "ClaimRewards", mock.Anything)
			}
		})
	}
}
