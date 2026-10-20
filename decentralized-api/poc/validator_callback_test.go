package poc

import (
	"context"
	"encoding/base64"
	"testing"

	"decentralized-api/broker"
	"decentralized-api/internal/callbackauth"
	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func minCountPocParams(modelID string, minCount uint32) *types.PocParams {
	statTest := types.DefaultPoCStatTestParams()
	statTest.MinCount = minCount
	return &types.PocParams{
		ValidationSampleSize: 200,
		Models: []*types.PoCModelConfig{{
			ModelId:           modelID,
			SeqLen:            256,
			StatTest:          statTest,
			WeightScaleFactor: types.DecimalFromFloat(1.0),
		}},
	}
}

func dispatchOne(t *testing.T, v *OffChainValidator, modelID string, pocParams *types.PocParams, verified []VerifiedArtifact) (validateResult, *mlnodeclient.MockClient) {
	t.Helper()
	mockClient := mlnodeclient.NewMockClient()
	v.nodeBroker = &stubNodeBroker{client: mockClient}
	node := broker.NodeResponse{Node: broker.Node{
		Host:    "127.0.0.1",
		PoCPort: 8080,
		NodeNum: 1,
		Models:  map[string]broker.ModelArgs{modelID: {}},
	}}
	work := &participantWork{
		address:  "cosmos1test",
		modelId:  modelID,
		pubKey:   "02AB",
		count:    400,
		url:      "http://participant",
		verified: verified,
	}
	nodeCounter := 0
	result := v.dispatchToMLNode(context.Background(), work, []broker.NodeResponse{node}, &nodeCounter, 1000, "start-hash", pocParams)
	return result, mockClient
}

func TestDispatchToMLNodeBindsCallbackToRequest(t *testing.T) {
	const modelID = "org/model-b"
	verified := []VerifiedArtifact{{Nonce: 1}, {Nonce: 2}, {Nonce: 3}}
	v := &OffChainValidator{callbackUrl: "http://callback", callbackSigner: testCallbackSigner}

	result, mockClient := dispatchOne(t, v, modelID, minCountPocParams(modelID, 200), verified)
	require.Equal(t, validateDispatched, result)

	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	req := mockClient.LastGenerateV2Req
	require.NotNil(t, req)

	token := testCallbackSigner.ValidationToken(1000, modelID, "02ab", 3, 200)
	require.Equal(t, "http://callback/v2/poc-batches/org%252Fmodel-b"+callbackauth.ValidationPath(3, 200, token), req.URL)
	require.Equal(t, uint32(200), req.StatTest.MinCount)
	require.True(t, testCallbackSigner.VerifyValidation(token, req.BlockHeight, modelID, req.PublicKey, int64(len(req.Nonces)), req.StatTest.MinCount))
}

func TestDispatchToMLNodeAbstainsWithoutSigner(t *testing.T) {
	const modelID = "model-a"
	v := &OffChainValidator{callbackUrl: "http://callback"}

	result, mockClient := dispatchOne(t, v, modelID, minCountPocParams(modelID, 0), []VerifiedArtifact{{Nonce: 1}})
	require.Equal(t, validateAbstain, result)

	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	require.Equal(t, 0, mockClient.GenerateV2Called)
}

func TestBelowMinCommitCount(t *testing.T) {
	params := minCountPocParams("model-a", 200)
	require.True(t, belowMinCommitCount(params, "model-a", 199))
	require.False(t, belowMinCommitCount(params, "model-a", 200))
	require.False(t, belowMinCommitCount(params, "model-b", 4))
	require.False(t, belowMinCommitCount(minCountPocParams("model-a", 0), "model-a", 4))
	require.False(t, belowMinCommitCount(nil, "model-a", 4))
}

type challengeQueryClient struct {
	types.QueryClient
	participantCalls int
}

func (c *challengeQueryClient) Participant(_ context.Context, req *types.QueryGetParticipantRequest, _ ...grpc.CallOption) (*types.QueryGetParticipantResponse, error) {
	c.participantCalls++
	return &types.QueryGetParticipantResponse{Participant: types.Participant{Index: req.Index, InferenceUrl: "http://participant"}}, nil
}

func (c *challengeQueryClient) AccountByAddress(_ context.Context, _ *types.QueryAccountByAddressRequest, _ ...grpc.CallOption) (*types.QueryAccountByAddressResponse, error) {
	return &types.QueryAccountByAddressResponse{Pubkey: base64.StdEncoding.EncodeToString([]byte{2, 0xab})}, nil
}

func TestChallengeWorkItemsSkipsCommitsBelowMinCount(t *testing.T) {
	queryClient := &challengeQueryClient{}
	ch := &types.OpenPoCChallenge{
		Challenge: &types.PoCChallenge{Target: "cosmos1target", StartHeight: 500},
		Commits: []*types.PoCV2StoreCommit{
			{ParticipantAddress: "cosmos1target", ModelId: "model-a", Count: 4},
			{ParticipantAddress: "cosmos1target", ModelId: "model-b", Count: 4},
			{ParticipantAddress: "cosmos1target", ModelId: "model-a", Count: 1200},
		},
	}
	params := minCountPocParams("model-a", 200)

	items := (&OffChainValidator{}).challengeWorkItems(queryClient, ch, params)

	require.Len(t, items, 2)
	require.Equal(t, "model-b", items[0].modelId)
	require.Equal(t, uint32(1200), items[1].count)
	require.Equal(t, 2, queryClient.participantCalls)
}
