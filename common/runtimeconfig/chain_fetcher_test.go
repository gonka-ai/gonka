package runtimeconfig

import (
	"context"
	"testing"

	"common/chain"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type fakeInferenceClient struct {
	params *inferencetypes.QueryParamsResponse
	epoch  *inferencetypes.QueryEpochInfoResponse
}

func (f *fakeInferenceClient) Params(context.Context, *inferencetypes.QueryParamsRequest, ...grpc.CallOption) (*inferencetypes.QueryParamsResponse, error) {
	return f.params, nil
}

func (f *fakeInferenceClient) EpochInfo(context.Context, *inferencetypes.QueryEpochInfoRequest, ...grpc.CallOption) (*inferencetypes.QueryEpochInfoResponse, error) {
	return f.epoch, nil
}

func (f *fakeInferenceClient) GetCurrentEpoch(context.Context, *inferencetypes.QueryGetCurrentEpochRequest, ...grpc.CallOption) (*inferencetypes.QueryGetCurrentEpochResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) ParticipantsWithBalances(context.Context, *inferencetypes.QueryParticipantsWithBalancesRequest, ...grpc.CallOption) (*inferencetypes.QueryParticipantsWithBalancesResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) AccountByAddress(context.Context, *inferencetypes.QueryAccountByAddressRequest, ...grpc.CallOption) (*inferencetypes.QueryAccountByAddressResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) Participant(context.Context, *inferencetypes.QueryGetParticipantRequest, ...grpc.CallOption) (*inferencetypes.QueryGetParticipantResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) DevshardEscrow(context.Context, *inferencetypes.QueryGetDevshardEscrowRequest, ...grpc.CallOption) (*inferencetypes.QueryGetDevshardEscrowResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) GranteesByMessageType(context.Context, *inferencetypes.QueryGranteesByMessageTypeRequest, ...grpc.CallOption) (*inferencetypes.QueryGranteesByMessageTypeResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) ExcludedParticipants(context.Context, *inferencetypes.QueryExcludedParticipantsRequest, ...grpc.CallOption) (*inferencetypes.QueryExcludedParticipantsResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) PocBatchesForStage(context.Context, *inferencetypes.QueryPocBatchesForStageRequest, ...grpc.CallOption) (*inferencetypes.QueryPocBatchesForStageResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) BridgeAddressesByChain(context.Context, *inferencetypes.QueryBridgeAddressesByChainRequest, ...grpc.CallOption) (*inferencetypes.QueryBridgeAddressesByChainResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) CurrentEpochGroupData(context.Context, *inferencetypes.QueryCurrentEpochGroupDataRequest, ...grpc.CallOption) (*inferencetypes.QueryCurrentEpochGroupDataResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) EpochGroupData(context.Context, *inferencetypes.QueryGetEpochGroupDataRequest, ...grpc.CallOption) (*inferencetypes.QueryGetEpochGroupDataResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) ModelsAll(context.Context, *inferencetypes.QueryModelsAllRequest, ...grpc.CallOption) (*inferencetypes.QueryModelsAllResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) GetAllModelPerTokenPrices(context.Context, *inferencetypes.QueryGetAllModelPerTokenPricesRequest, ...grpc.CallOption) (*inferencetypes.QueryGetAllModelPerTokenPricesResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) GetAllModelCapacities(context.Context, *inferencetypes.QueryGetAllModelCapacitiesRequest, ...grpc.CallOption) (*inferencetypes.QueryGetAllModelCapacitiesResponse, error) {
	panic("not implemented")
}
func (f *fakeInferenceClient) PreservedNodesSnapshot(context.Context, *inferencetypes.QueryPreservedNodesSnapshotRequest, ...grpc.CallOption) (*inferencetypes.QueryPreservedNodesSnapshotResponse, error) {
	panic("not implemented")
}

var _ chain.InferenceClient = (*fakeInferenceClient)(nil)

func TestChainFetcher_FetchSnapshot(t *testing.T) {
	fetcher := NewChainFetcherFromClient(&fakeInferenceClient{
		params: &inferencetypes.QueryParamsResponse{
			Params: inferencetypes.Params{
				ValidationParams: &inferencetypes.ValidationParams{LogprobsMode: "raw"},
				DevshardEscrowParams: &inferencetypes.DevshardEscrowParams{
					DevshardRequestsEnabled: true,
					MaxNonce:                500,
					RefusalTimeout:          60,
					ExecutionTimeout:        1200,
					ValidationRate:          6000,
					VoteThresholdFactor:     50,
				},
			},
		},
		epoch: &inferencetypes.QueryEpochInfoResponse{
			LatestEpoch: inferencetypes.Epoch{Index: 12, PocStartBlockHeight: 1200},
		},
	})

	snap, err := fetcher.FetchSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(12), snap.CurrentEpochID)
	require.Equal(t, int64(1200), snap.ParamsBlockHeight)
	require.Equal(t, "raw", snap.LogprobsMode)
	require.True(t, snap.DevshardRequestsEnabled)
	require.Equal(t, uint32(6000), snap.ValidationRate)
	require.Equal(t, uint32(50), snap.VoteThresholdFactor)
}
