package queryapitest

import (
	"encoding/json"
	"net/http"
	"testing"

	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestProtoJSONHandlersEncodeWithoutPanic guards against assigning raw gogo protobuf
// messages to gen.RawProtoJson fields (encoding/json panics on Any pubkeys).
func TestProtoJSONHandlersEncodeWithoutPanic(t *testing.T) {
	t.Run("GET /v1/epochs/latest", func(t *testing.T) {
		h := handlersWithInference(t, &stubEpochServer{})
		ctx, rec := echoContext(t, http.MethodGet, "/v1/epochs/latest")
		require.NoError(t, h.GetEpoch(ctx, "latest"))
		require.Equal(t, http.StatusOK, rec.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Contains(t, body, "epoch_params")
	})

	t.Run("GET /v1/bls/epoch/1", func(t *testing.T) {
		h := handlersWithBLS(t, &stubBLSEpochServer{})
		ctx, rec := echoContext(t, http.MethodGet, "/v1/bls/epoch/1")
		require.NoError(t, h.GetBLSEpoch(ctx, 1))
		require.Equal(t, http.StatusOK, rec.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Contains(t, body, "epoch_data")
	})

	t.Run("GET /v1/bls/signatures/deadbeef", func(t *testing.T) {
		h := handlersWithBLS(t, &stubBLSSignatureServer{
			req: &blstypes.ThresholdSigningRequest{
				Status: blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_PENDING_SIGNING,
			},
		})
		ctx, rec := echoContext(t, http.MethodGet, "/v1/bls/signatures/deadbeef")
		require.NoError(t, h.GetBLSSignature(ctx, "deadbeef"))
		require.Equal(t, http.StatusOK, rec.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Contains(t, body, "signing_request")
	})

	t.Run("GET /v1/epochs/1/participants", func(t *testing.T) {
		srv := &stubEpochParticipantsComet{withValidators: true}
		h := handlersWithInferenceAndComet(t, &stubEpochParticipantsInference{}, srv)
		ctx, rec := echoContext(t, http.MethodGet, "/v1/epochs/1/participants")
		require.NoError(t, h.GetEpochParticipants(ctx, "1"))
		require.Equal(t, http.StatusOK, rec.Code)

		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

		validators, ok := body["validators"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, validators)

		val, ok := validators[0].(map[string]any)
		require.True(t, ok)
		pubKey, ok := val["pub_key"].(string)
		require.True(t, ok, "pub_key must be a string, got %T", val["pub_key"])
		require.NotEmpty(t, pubKey)
	})

	t.Run("GET /v1/governance/models", func(t *testing.T) {
		h := handlersWithInference(t, &stubGovernanceModelsServer{
			models: []inferencetypes.Model{makeModel("gov", 1)},
		})
		ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/models")
		require.NoError(t, h.GetGovernanceModels(ctx))
		require.Equal(t, http.StatusOK, rec.Code)
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &map[string]any{}))
	})
}
