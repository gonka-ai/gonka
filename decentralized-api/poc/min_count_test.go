package poc

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func minCountPocParams(modelID string, minCount uint32) *types.PocParams {
	statTest := types.DefaultPoCStatTestParams()
	statTest.MinCount = minCount
	return &types.PocParams{
		ValidationSampleSize: 200,
		Models:               []*types.PoCModelConfig{{ModelId: modelID, StatTest: statTest}},
	}
}

func TestBelowMinCommitCount(t *testing.T) {
	params := minCountPocParams("model-a", 200)
	require.True(t, belowMinCommitCount(params, "model-a", 199))
	require.False(t, belowMinCommitCount(params, "model-a", 200))
	require.False(t, belowMinCommitCount(params, "model-b", 4))
	require.False(t, belowMinCommitCount(minCountPocParams("model-a", 0), "model-a", 4))
	require.False(t, belowMinCommitCount(nil, "model-a", 4))
}

type minCountQueryClient struct {
	types.QueryClient
	participantCalls int
}

func (c *minCountQueryClient) Participant(_ context.Context, req *types.QueryGetParticipantRequest, _ ...grpc.CallOption) (*types.QueryGetParticipantResponse, error) {
	c.participantCalls++
	return &types.QueryGetParticipantResponse{Participant: types.Participant{Index: req.Index, InferenceUrl: "http://participant"}}, nil
}

func (c *minCountQueryClient) AccountByAddress(_ context.Context, _ *types.QueryAccountByAddressRequest, _ ...grpc.CallOption) (*types.QueryAccountByAddressResponse, error) {
	return &types.QueryAccountByAddressResponse{Pubkey: base64.StdEncoding.EncodeToString([]byte{2, 0xab})}, nil
}

func TestChallengeWorkItemsSkipsCommitsBelowMinCount(t *testing.T) {
	queryClient := &minCountQueryClient{}
	ch := &types.OpenPoCChallenge{
		Challenge: &types.PoCChallenge{Target: "cosmos1target", StartHeight: 500},
		Commits: []*types.PoCV2StoreCommit{
			{ParticipantAddress: "cosmos1target", ModelId: "model-a", Count: 4},
			{ParticipantAddress: "cosmos1target", ModelId: "model-b", Count: 4},
			{ParticipantAddress: "cosmos1target", ModelId: "model-a", Count: 1200},
		},
	}

	items := (&OffChainValidator{}).challengeWorkItems(queryClient, ch, minCountPocParams("model-a", 200))

	require.Len(t, items, 2)
	require.Equal(t, "model-b", items[0].modelId)
	require.Equal(t, uint32(1200), items[1].count)
	require.Equal(t, 2, queryClient.participantCalls)
}
