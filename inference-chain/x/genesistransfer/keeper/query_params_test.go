package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/genesistransfer/types"
)

func TestParamsQuery(t *testing.T) {
	keeper, ctx := keepertest.GenesistransferKeeper(t)
	params := types.DefaultParams()
	require.NoError(t, keeper.SetParams(ctx, params))

	response, err := keeper.Params(ctx, &types.QueryParamsRequest{})
	require.NoError(t, err)
	require.Equal(t, &types.QueryParamsResponse{Params: params}, response)
}

func TestTransferStatusQuery(t *testing.T) {
	keeper, ctx := keepertest.GenesistransferKeeper(t)

	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	t.Run("nil request", func(t *testing.T) {
		_, err := keeper.TransferStatus(ctx, nil)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("empty address", func(t *testing.T) {
		_, err := keeper.TransferStatus(ctx, &types.QueryTransferStatusRequest{GenesisAddress: ""})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("malformed address", func(t *testing.T) {
		_, err := keeper.TransferStatus(ctx, &types.QueryTransferStatusRequest{GenesisAddress: "not-a-bech32-address"})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("not transferred", func(t *testing.T) {
		resp, err := keeper.TransferStatus(ctx, &types.QueryTransferStatusRequest{GenesisAddress: genesisAddr.String()})
		require.NoError(t, err)
		require.False(t, resp.IsTransferred)
		require.Nil(t, resp.TransferRecord)
	})

	t.Run("transferred", func(t *testing.T) {
		record := types.TransferRecord{
			GenesisAddress:    genesisAddr.String(),
			RecipientAddress:  recipientAddr.String(),
			TransferHeight:    42,
			Completed:         true,
			TransferredDenoms: []string{"ngonka"},
			TransferAmount:    "1000ngonka",
		}
		require.NoError(t, keeper.SetTransferRecord(ctx, record))

		resp, err := keeper.TransferStatus(ctx, &types.QueryTransferStatusRequest{GenesisAddress: genesisAddr.String()})
		require.NoError(t, err)
		require.True(t, resp.IsTransferred)
		require.NotNil(t, resp.TransferRecord)
		require.Equal(t, record, *resp.TransferRecord)

		stored, found, err := keeper.GetTransferRecord(ctx, genesisAddr)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, *stored, *resp.TransferRecord)

		err = keeper.ValidateTransfer(ctx, genesisAddr, recipientAddr)
		require.ErrorIs(t, err, types.ErrAlreadyTransferred)
	})
}
