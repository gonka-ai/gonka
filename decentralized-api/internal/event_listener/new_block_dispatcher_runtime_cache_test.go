package event_listener

import (
	"context"
	"testing"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/internal/validation"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type mockParamsQueryClient struct {
	mock.Mock
}

func (m *mockParamsQueryClient) EpochInfo(ctx context.Context, req *types.QueryEpochInfoRequest, opts ...grpc.CallOption) (*types.QueryEpochInfoResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryEpochInfoResponse), args.Error(1)
}

func (m *mockParamsQueryClient) Params(ctx context.Context, req *types.QueryParamsRequest, opts ...grpc.CallOption) (*types.QueryParamsResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryParamsResponse), args.Error(1)
}

func newRuntimeCacheTestDispatcher(t *testing.T, qc *mockParamsQueryClient) (*OnNewBlockDispatcher, *apiconfig.ConfigManager) {
	t.Helper()

	cm := &apiconfig.ConfigManager{}
	cm.EnsureRuntimeConfigNotifier()
	phaseTracker := &chainphase.ChainPhaseTracker{}

	qc.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{
		Params: types.Params{EpochParams: &defaultEpochParams},
		LatestEpoch: types.Epoch{
			Index:               3,
			PocStartBlockHeight: 1,
		},
		BlockHeight: 100,
	}, nil)

	mockSeedManager := &MockRandomSeedManager{}
	mockSeedManager.On("ChangeCurrentSeed").Return()
	mockSeedManager.On("RequestMoney", mock.AnythingOfType("uint64")).Return()
	mockSeedManager.On("GenerateSeedInfo", mock.AnythingOfType("uint64")).Return()
	mockSeedManager.On("CreateNewSeed", mock.AnythingOfType("uint64")).Return(nil, nil)
	mockSeedManager.On("GetSeedForEpoch", mock.AnythingOfType("uint64")).Return(apiconfig.SeedInfo{})

	dispatcher := NewOnNewBlockDispatcher(
		nil,
		&MockOffChainValidator{},
		qc,
		phaseTracker,
		func() (*coretypes.ResultStatus, error) {
			return &coretypes.ResultStatus{
				SyncInfo: coretypes.SyncInfo{CatchingUp: false},
			}, nil
		},
		func(blockHeight int64) error { return nil },
		mockSeedManager,
		defaultReconciliationConfig,
		cm,
		&validation.InferenceValidator{},
	)
	return dispatcher, cm
}

func devshardParamsResponse(
	enabled bool,
	maxNonce uint32,
) *types.QueryParamsResponse {
	return devshardParamsResponseFull(enabled, maxNonce, 60, 1200, 5000, 50)
}

func devshardParamsResponseFull(
	enabled bool,
	maxNonce uint32,
	refusalTimeout, executionTimeout int64,
	validationRate, voteThresholdFactor uint32,
) *types.QueryParamsResponse {
	return &types.QueryParamsResponse{
		Params: types.Params{
			ValidationParams: &types.ValidationParams{
				TimestampExpiration: 10,
				TimestampAdvance:    10,
				ExpirationBlocks:    10,
				LogprobsMode:        "raw",
			},
			DevshardEscrowParams: &types.DevshardEscrowParams{
				DevshardRequestsEnabled: enabled,
				MaxNonce:                maxNonce,
				RefusalTimeout:                    refusalTimeout,
				ExecutionTimeout:                  executionTimeout,
				ValidationRate:                    validationRate,
				VoteThresholdFactor:               voteThresholdFactor,
				ApprovedVersions: []*types.DevshardApprovedVersion{
					{Name: "v1", Binary: "https://example/v1", Sha256: "sha1"},
				},
			},
		},
	}
}

func TestOnNewBlockDispatcher_UpdatesRuntimeCache(t *testing.T) {
	qc := &mockParamsQueryClient{}
	dispatcher, cm := newRuntimeCacheTestDispatcher(t, qc)

	qc.On("Params", mock.Anything, mock.Anything).Return(
		devshardParamsResponse(true, 20000), nil,
	).Once()
	err := dispatcher.ProcessNewBlock(context.Background(), chainphase.BlockInfo{
		Height: 100,
		Hash:   "real-block-hash-1",
	})
	require.NoError(t, err)

	got := cm.GetDevshardVersions()
	require.True(t, got.DevshardRequestsEnabled)
	require.Equal(t, uint32(20000), got.MaxNonce)
	require.Equal(t, int64(60), got.RefusalTimeout)
	require.Equal(t, int64(1200), got.ExecutionTimeout)
	require.Equal(t, uint32(5000), got.ValidationRate)
	require.Equal(t, uint32(50), got.VoteThresholdFactor)
	require.Len(t, got.Versions, 1)
	require.Equal(t, "v1", got.Versions[0].Name)
	require.Equal(t, int64(100), cm.RuntimeParamsBlockHeight())

	qc.On("Params", mock.Anything, mock.Anything).Return(
		devshardParamsResponse(false, 30000), nil,
	).Once()
	err = dispatcher.ProcessNewBlock(context.Background(), chainphase.BlockInfo{
		Height: 101,
		Hash:   "real-block-hash-2",
	})
	require.NoError(t, err)

	got = cm.GetDevshardVersions()
	require.False(t, got.DevshardRequestsEnabled)
	require.Equal(t, uint32(30000), got.MaxNonce)
	require.Equal(t, int64(101), cm.RuntimeParamsBlockHeight())
}

func TestOnNewBlockDispatcher_NoNotifyOnUnchangedBlock(t *testing.T) {
	qc := &mockParamsQueryClient{}
	dispatcher, cm := newRuntimeCacheTestDispatcher(t, qc)

	resp := devshardParamsResponse(true, 20000)
	qc.On("Params", mock.Anything, mock.Anything).Return(resp, nil).Twice()

	require.NoError(t, dispatcher.ProcessNewBlock(context.Background(), chainphase.BlockInfo{
		Height: 300,
		Hash:   "unchanged-block-1",
	}))
	require.Equal(t, int64(300), cm.RuntimeParamsBlockHeight())

	ch := cm.RuntimeConfigNotifier().NotifyChan()
	require.NoError(t, dispatcher.ProcessNewBlock(context.Background(), chainphase.BlockInfo{
		Height: 301,
		Hash:   "unchanged-block-2",
	}))
	select {
	case <-ch:
		t.Fatal("expected no notify when runtime params and epoch unchanged")
	case <-time.After(50 * time.Millisecond):
	}
	require.Equal(t, int64(300), cm.RuntimeParamsBlockHeight())
}

func TestOnNewBlockDispatcher_ApplyRuntimeConfigBlockIfChanged_Notifies(t *testing.T) {
	qc := &mockParamsQueryClient{}
	dispatcher, cm := newRuntimeCacheTestDispatcher(t, qc)

	qc.On("Params", mock.Anything, mock.Anything).Return(
		devshardParamsResponse(true, 20000), nil,
	).Once()

	ch := cm.RuntimeConfigNotifier().NotifyChan()
	require.NoError(t, dispatcher.ProcessNewBlock(context.Background(), chainphase.BlockInfo{
		Height: 200,
		Hash:   "notify-block",
	}))

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected notifier wake after block param sync")
	}
	require.Equal(t, int64(200), cm.RuntimeParamsBlockHeight())
}

func TestOnNewBlockDispatcher_NilDevshardEscrowParams_NoPanic(t *testing.T) {
	qc := &mockParamsQueryClient{}
	dispatcher, cm := newRuntimeCacheTestDispatcher(t, qc)

	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		DevshardRequestsEnabled: true,
		MaxNonce:                100,
	})

	qc.On("Params", mock.Anything, mock.Anything).Return(&types.QueryParamsResponse{
		Params: types.Params{
			ValidationParams: &types.ValidationParams{
				TimestampExpiration: 10,
				TimestampAdvance:    10,
				ExpirationBlocks:    10,
				LogprobsMode:        "raw",
			},
			DevshardEscrowParams: nil,
		},
	}, nil).Once()

	require.NotPanics(t, func() {
		err := dispatcher.ProcessNewBlock(context.Background(), chainphase.BlockInfo{
			Height: 102,
			Hash:   "real-block-hash-nil-escrow",
		})
		require.NoError(t, err)
	})

	got := cm.GetDevshardVersions()
	require.True(t, got.DevshardRequestsEnabled)
	require.Equal(t, uint32(100), got.MaxNonce)
}
