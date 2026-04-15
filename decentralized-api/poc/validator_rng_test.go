package poc

import (
	"context"
	"testing"

	"decentralized-api/broker"
	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubProofFetcher implements proofFetcher and returns a fixed set of artifacts.
type stubProofFetcher struct {
	artifacts []VerifiedArtifact
}

func (s *stubProofFetcher) FetchAndVerifyProofs(_ context.Context, _ string, _ ProofRequest) ([]VerifiedArtifact, error) {
	return s.artifacts, nil
}

// stubNodeBroker implements nodeBrokerFacade for tests.
type stubNodeBroker struct {
	client mlnodeclient.MLNodeClient
}

func (s *stubNodeBroker) NewNodeClient(_ *broker.Node) mlnodeclient.MLNodeClient {
	return s.client
}

func (s *stubNodeBroker) GetNodes() ([]broker.NodeResponse, error) {
	return nil, nil
}

// runValidateParticipant is a helper that runs validateParticipant with fixed
// test fixtures and returns the captured GenerateV2 request.
func runValidateParticipant(t *testing.T, pocStrongerRng bool) *mlnodeclient.PoCGenerateRequestV2 {
	t.Helper()
	mockClient := mlnodeclient.NewMockClient()

	v := &OffChainValidator{
		nodeBroker:  &stubNodeBroker{client: mockClient},
		callbackUrl: "http://callback",
	}

	testNode := broker.NodeResponse{
		Node: broker.Node{
			Host:    "127.0.0.1",
			PoCPort: 8080,
			NodeNum: 1,
			Models: map[string]broker.ModelArgs{
				"test-model": {},
			},
		},
	}

	// Stub returns one artifact; nonce=1, count=100 → porosity=0.01, well below threshold.
	stub := &stubProofFetcher{
		artifacts: []VerifiedArtifact{{LeafIndex: 0, Nonce: 1, VectorB64: ""}},
	}

	work := participantWork{
		address: "cosmos1test",
		modelId: "test-model",
		pubKey:  "testpubkey",
		count:   100,
		url:     "http://participant",
	}

	pocParams := &types.PocParams{
		PocStrongerRngEnabled: pocStrongerRng,
		Models: []*types.PoCModelConfig{
			{
				ModelId:           "test-model",
				SeqLen:            256,
				StatTest:          types.DefaultPoCStatTestParams(),
				WeightScaleFactor: types.DecimalFromFloat(1.0),
			},
		},
	}

	nodeCounter := 0
	result := v.validateParticipant(
		0, work, stub,
		[]broker.NodeResponse{testNode}, &nodeCounter,
		1000, "sampling-hash", "start-hash",
		pocParams, 1,
	)

	require.Equal(t, validateSuccess, result)

	mockClient.Mu.Lock()
	require.Equal(t, 1, mockClient.GenerateV2Called, "GenerateV2 should be called once")
	require.NotNil(t, mockClient.LastGenerateV2Req)
	req := *mockClient.LastGenerateV2Req
	mockClient.Mu.Unlock()
	return &req
}

// TestValidateParticipant_StrongerRngPropagated asserts that PocStrongerRng from
// PocParams reaches the GenerateV2 call sent to the ML node during validation.
// This is the validation (validator.go) path, analogous to
// TestStartPoCNodeCommandV2_StrongerRngPropagated for the broker (InitGenerateV2) path.
func TestValidateParticipant_StrongerRngPropagated(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		req := runValidateParticipant(t, true)
		assert.True(t, req.PocStrongerRng, "PocStrongerRng must be forwarded to GenerateV2 when enabled")
	})

	t.Run("disabled", func(t *testing.T) {
		req := runValidateParticipant(t, false)
		assert.False(t, req.PocStrongerRng, "PocStrongerRng must be forwarded to GenerateV2 when disabled")
	})
}
