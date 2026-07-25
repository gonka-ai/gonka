package grpcface_test

import (
	"context"
	"encoding/hex"
	"net/http/httptest"
	"testing"

	"common/chain"
	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockchain/fetcher"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/store"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func startTestServer(t *testing.T, st *store.Store) *chain.Client {
	t.Helper()
	srv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: st})
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return chain.NewFromConn(conn)
}

func TestMockChainGRPC_Phase3aQueries(t *testing.T) {
	st := seed.Defaults()
	client := startTestServer(t, st)
	ctx := context.Background()

	paramsResp, err := client.InferenceQueryClient().Params(ctx, &inferencetypes.QueryParamsRequest{})
	require.NoError(t, err)
	require.NotNil(t, paramsResp.Params.DevshardEscrowParams)
	require.True(t, paramsResp.Params.DevshardEscrowParams.DevshardRequestsEnabled)

	epochResp, err := client.InferenceQueryClient().EpochInfo(ctx, &inferencetypes.QueryEpochInfoRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(1), epochResp.LatestEpoch.Index)
	require.Equal(t, int64(100), epochResp.LatestEpoch.PocStartBlockHeight)

	curEpoch, err := client.InferenceQueryClient().GetCurrentEpoch(ctx, &inferencetypes.QueryGetCurrentEpochRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(1), curEpoch.Epoch)

	escrowResp, err := client.InferenceQueryClient().DevshardEscrow(ctx, &inferencetypes.QueryGetDevshardEscrowRequest{Id: 1})
	require.NoError(t, err)
	require.True(t, escrowResp.Found)
	require.Equal(t, uint64(1), escrowResp.Escrow.Id)

	host := "gonka1host000000000000000000000000000000000"
	partResp, err := client.InferenceQueryClient().Participant(ctx, &inferencetypes.QueryGetParticipantRequest{Index: host})
	require.NoError(t, err)
	require.Equal(t, host, partResp.Participant.Address)
	require.Contains(t, partResp.Participant.InferenceUrl, "versiond-router")

	egdResp, err := client.InferenceQueryClient().EpochGroupData(ctx, &inferencetypes.QueryGetEpochGroupDataRequest{
		EpochIndex: 1,
		ModelId:    "test-model",
	})
	require.NoError(t, err)
	require.NotNil(t, egdResp.EpochGroupData.ModelSnapshot.ValidationThreshold)
	require.Equal(t, int64(50), egdResp.EpochGroupData.ModelSnapshot.ValidationThreshold.Value)

	grantResp, err := client.InferenceQueryClient().GranteesByMessageType(ctx, &inferencetypes.QueryGranteesByMessageTypeRequest{
		GranterAddress: host,
		MessageTypeUrl: "/inference.inference.MsgStartInference",
	})
	require.NoError(t, err)
	require.Len(t, grantResp.Grantees, 1)

	blockResp, err := client.CometServiceClient().GetLatestBlock(ctx, &cmtservice.GetLatestBlockRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(150), blockResp.SdkBlock.Header.Height)
}

func TestMockChainGRPC_ChainFetcherSnapshot(t *testing.T) {
	st := seed.Defaults()
	client := startTestServer(t, st)

	admin := adminface.NewServer(st, nil, nil)
	adminHTTP := httptest.NewServer(admin.Handler())
	t.Cleanup(adminHTTP.Close)

	f := fetcher.New(client, adminface.NewClient(adminHTTP.URL))
	snap, err := f.FetchSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(1), snap.CurrentEpochID)
	require.Equal(t, int64(150), snap.ParamsBlockHeight)
	require.True(t, snap.DevshardRequestsEnabled)
	require.Equal(t, uint32(6000), snap.ValidationRate)
}

func TestMockChainGRPC_BridgeEscrowRoundTrip(t *testing.T) {
	st := seed.Defaults()
	client := startTestServer(t, st)

	resp, err := client.InferenceQueryClient().DevshardEscrow(context.Background(),
		&inferencetypes.QueryGetDevshardEscrowRequest{Id: 1})
	require.NoError(t, err)
	require.True(t, resp.Found)

	_, err = hex.DecodeString(resp.Escrow.AppHash)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Escrow.Slots)
}
