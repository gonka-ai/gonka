package devshard

import (
	"context"
	"testing"

	"devshard/bridge"

	inferenceTypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type testChainQueryClient struct {
	inferenceTypes.QueryClient
	mock.Mock
}

func (m *testChainQueryClient) DevshardEscrow(ctx context.Context, in *inferenceTypes.QueryGetDevshardEscrowRequest, opts ...grpc.CallOption) (*inferenceTypes.QueryGetDevshardEscrowResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*inferenceTypes.QueryGetDevshardEscrowResponse), args.Error(1)
}

func (m *testChainQueryClient) Params(ctx context.Context, in *inferenceTypes.QueryParamsRequest, opts ...grpc.CallOption) (*inferenceTypes.QueryParamsResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*inferenceTypes.QueryParamsResponse), args.Error(1)
}

type stubInferenceQueryProvider struct {
	qc inferenceTypes.QueryClient
}

func (p *stubInferenceQueryProvider) NewInferenceQueryClient() inferenceTypes.QueryClient {
	return p.qc
}

func TestChainBridgeStubs(t *testing.T) {
	cb := NewChainBridge(nil)

	assert.ErrorIs(t, cb.OnEscrowCreated(bridge.EscrowInfo{}), bridge.ErrNotImplemented)
	assert.ErrorIs(t, cb.OnSettlementProposed("1", nil, 0), bridge.ErrNotImplemented)
	assert.ErrorIs(t, cb.OnSettlementFinalized("1"), bridge.ErrNotImplemented)
	assert.ErrorIs(t, cb.SubmitDisputeState("1", nil, 0, nil), bridge.ErrNotImplemented)
}

func TestChainBridgeImplementsInterface(t *testing.T) {
	var _ bridge.MainnetBridge = (*ChainBridge)(nil)
	var _ bridge.SessionBindParamsBridge = (*ChainBridge)(nil)
}

func TestChainBridge_GetEscrow_FeesFromEscrow(t *testing.T) {
	qc := &testChainQueryClient{}
	qc.On("DevshardEscrow", mock.Anything, mock.Anything).Return(&inferenceTypes.QueryGetDevshardEscrowResponse{
		Found: true,
		Escrow: &inferenceTypes.DevshardEscrow{
			Id:                42,
			Creator:           "creator",
			Amount:            100,
			Slots:             []string{"a"},
			EpochIndex:        1,
			AppHash:           "cafe",
			TokenPrice:        2,
			CreateDevshardFee: 10_000,
			FeePerNonce:       1_000,
		},
	}, nil)

	cb := NewChainBridge(&stubInferenceQueryProvider{qc: qc})
	info, err := cb.GetEscrow("42")
	require.NoError(t, err)
	assert.Equal(t, uint64(10_000), info.CreateDevshardFee)
	assert.Equal(t, uint64(1_000), info.FeePerNonce)
	assert.Equal(t, uint64(2), info.TokenPrice)
	qc.AssertNotCalled(t, "Params", mock.Anything, mock.Anything)
}

func TestChainBridge_GetEscrow_DoesNotQueryParams(t *testing.T) {
	qc := &testChainQueryClient{}
	qc.On("DevshardEscrow", mock.Anything, mock.Anything).Return(&inferenceTypes.QueryGetDevshardEscrowResponse{
		Found: true,
		Escrow: &inferenceTypes.DevshardEscrow{
			Id:         1,
			Creator:    "creator",
			Amount:     1,
			Slots:      []string{"a", "b", "c"},
			EpochIndex: 0,
			AppHash:    "aa",
			TokenPrice: 1,
		},
	}, nil)

	cb := NewChainBridge(&stubInferenceQueryProvider{qc: qc})
	info, err := cb.GetEscrow("1")
	require.NoError(t, err)
	require.NotNil(t, info)
	qc.AssertNotCalled(t, "Params", mock.Anything, mock.Anything)
}

func TestChainBridge_GetSessionBindParams(t *testing.T) {
	qc := &testChainQueryClient{}
	qc.On("Params", mock.Anything, mock.Anything).Return(&inferenceTypes.QueryParamsResponse{
		Params: inferenceTypes.Params{
			DevshardEscrowParams: &inferenceTypes.DevshardEscrowParams{
				ValidationRate:      0,
				VoteThresholdFactor: 50,
				RefusalTimeout:      60,
				ExecutionTimeout:    1200,
			},
		},
	}, nil)

	cb := NewChainBridge(&stubInferenceQueryProvider{qc: qc})
	live, err := cb.GetSessionBindParams()
	require.NoError(t, err)
	assert.Equal(t, uint32(0), live.ValidationRate)
	assert.Equal(t, uint32(50), live.VoteThresholdFactor)
	assert.Equal(t, int64(60), live.RefusalTimeout)
	assert.Equal(t, int64(1200), live.ExecutionTimeout)
}
