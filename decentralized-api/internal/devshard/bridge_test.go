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
}

func TestChainBridge_GetEscrow_SealGraceFromParams(t *testing.T) {
	qc := &testChainQueryClient{}
	qc.On("DevshardEscrow", mock.Anything, mock.Anything).Return(&inferenceTypes.QueryGetDevshardEscrowResponse{
		Found: true,
		Escrow: &inferenceTypes.DevshardEscrow{
			Id:         42,
			Creator:    "creator",
			Amount:     100,
			Slots:      []string{"a", "b", "c"},
			EpochIndex: 7,
			AppHash:    "cafe",
			TokenPrice: 1,
		},
	}, nil)
	qc.On("Params", mock.Anything, mock.Anything).Return(&inferenceTypes.QueryParamsResponse{
		Params: inferenceTypes.Params{
			DevshardEscrowParams: &inferenceTypes.DevshardEscrowParams{
				DefaultSealGraceNonces: 88,
			},
		},
	}, nil)

	cb := NewChainBridge(&stubInferenceQueryProvider{qc: qc})
	info, err := cb.GetEscrow("42")
	require.NoError(t, err)
	assert.Equal(t, uint32(88), info.SealGraceNonces)
}

func TestChainBridge_GetEscrow_ParamsError_LeavesSealGraceZero(t *testing.T) {
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
	qc.On("Params", mock.Anything, mock.Anything).Return(nil, assert.AnError)

	cb := NewChainBridge(&stubInferenceQueryProvider{qc: qc})
	info, err := cb.GetEscrow("1")
	require.NoError(t, err)
	assert.Equal(t, uint32(0), info.SealGraceNonces)
}

func TestChainBridge_GetEscrow_DefaultSealGraceZero_UsesGroupFallback(t *testing.T) {
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
	qc.On("Params", mock.Anything, mock.Anything).Return(&inferenceTypes.QueryParamsResponse{
		Params: inferenceTypes.Params{
			DevshardEscrowParams: &inferenceTypes.DevshardEscrowParams{
				DefaultSealGraceNonces: 0,
			},
		},
	}, nil)

	cb := NewChainBridge(&stubInferenceQueryProvider{qc: qc})
	info, err := cb.GetEscrow("1")
	require.NoError(t, err)
	assert.Equal(t, uint32(30), info.SealGraceNonces)
}
