package participant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type fakeActivityQuerySource struct {
	client *fakeActivityQueryClient
}

func (f fakeActivityQuerySource) NewInferenceQueryClient() types.QueryClient {
	return f.client
}

type fakeActivityQueryClient struct {
	types.QueryClient

	parentResp     *types.QueryCurrentEpochGroupDataResponse
	parentErr      error
	subgroupResp   map[string]*types.QueryGetEpochGroupDataResponse
	subgroupErr    map[string]error
	blockUntilDone bool
}

func (f *fakeActivityQueryClient) CurrentEpochGroupData(
	ctx context.Context,
	_ *types.QueryCurrentEpochGroupDataRequest,
	_ ...grpc.CallOption,
) (*types.QueryCurrentEpochGroupDataResponse, error) {
	if f.blockUntilDone {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.parentErr != nil {
		return nil, f.parentErr
	}
	return f.parentResp, nil
}

func (f *fakeActivityQueryClient) EpochGroupData(
	_ context.Context,
	req *types.QueryGetEpochGroupDataRequest,
	_ ...grpc.CallOption,
) (*types.QueryGetEpochGroupDataResponse, error) {
	if err := f.subgroupErr[req.ModelId]; err != nil {
		return nil, err
	}
	return f.subgroupResp[req.ModelId], nil
}

func newActivityTrackerForTest(client *fakeActivityQueryClient) *ActivityTracker {
	return &ActivityTracker{
		queryClient:    fakeActivityQuerySource{client: client},
		address:        "participant-1",
		interval:       time.Hour,
		refreshTimeout: time.Second,
	}
}

func TestActivityTracker_refresh_marksKnownActive_whenParticipantHasMLNode(t *testing.T) {
	// Given
	tracker := newActivityTrackerForTest(&fakeActivityQueryClient{
		parentResp: parentGroup("model-a"),
		subgroupResp: map[string]*types.QueryGetEpochGroupDataResponse{
			"model-a": subgroup("participant-1", "node-1"),
		},
	})

	// When
	tracker.refresh(context.Background())

	// Then
	require.True(t, tracker.IsKnown())
	require.True(t, tracker.IsActive())
}

func TestActivityTracker_refresh_marksKnownInactive_whenParticipantAbsent(t *testing.T) {
	// Given
	tracker := newActivityTrackerForTest(&fakeActivityQueryClient{
		parentResp: parentGroup("model-a"),
		subgroupResp: map[string]*types.QueryGetEpochGroupDataResponse{
			"model-a": subgroup("other-participant", "node-2"),
		},
	})

	// When
	tracker.refresh(context.Background())

	// Then
	require.True(t, tracker.IsKnown())
	require.False(t, tracker.IsActive())
}

func TestActivityTracker_refresh_keepsUnknown_whenInitialQueryFails(t *testing.T) {
	// Given
	tracker := newActivityTrackerForTest(&fakeActivityQueryClient{
		parentErr: errors.New("chain unavailable"),
	})

	// When
	tracker.refresh(context.Background())

	// Then
	require.False(t, tracker.IsKnown())
	require.False(t, tracker.IsActive())
}

func TestActivityTracker_refresh_preservesPreviousState_whenSubgroupQueryFails(t *testing.T) {
	// Given
	client := &fakeActivityQueryClient{
		parentResp: parentGroup("model-a"),
		subgroupResp: map[string]*types.QueryGetEpochGroupDataResponse{
			"model-a": subgroup("participant-1", "node-1"),
		},
	}
	tracker := newActivityTrackerForTest(client)
	tracker.refresh(context.Background())
	require.True(t, tracker.IsActive())

	client.subgroupResp = map[string]*types.QueryGetEpochGroupDataResponse{}
	client.subgroupErr = map[string]error{"model-a": errors.New("subgroup timeout")}

	// When
	tracker.refresh(context.Background())

	// Then
	require.True(t, tracker.IsKnown())
	require.True(t, tracker.IsActive())
}

func TestActivityTracker_refresh_returnsAfterTimeout_whenQueryHangs(t *testing.T) {
	// Given
	tracker := newActivityTrackerForTest(&fakeActivityQueryClient{
		blockUntilDone: true,
	})
	tracker.refreshTimeout = 20 * time.Millisecond

	// When
	started := time.Now()
	tracker.refresh(context.Background())

	// Then
	require.Less(t, time.Since(started), 500*time.Millisecond)
	require.False(t, tracker.IsKnown())
	require.False(t, tracker.IsActive())
}

func parentGroup(modelID string) *types.QueryCurrentEpochGroupDataResponse {
	return &types.QueryCurrentEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:     7,
			SubGroupModels: []string{modelID},
		},
	}
}

func subgroup(participantAddress, nodeID string) *types.QueryGetEpochGroupDataResponse {
	return &types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress: participantAddress,
					MlNodes: []*types.MLNodeInfo{
						{NodeId: nodeID},
					},
				},
			},
		},
	}
}
