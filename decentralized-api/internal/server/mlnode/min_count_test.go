package mlnode

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestValidatedCallbackAbstainsOnInsufficientSample(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	server := NewServer(mockRecorder, newMLNodeTestBroker(t, types.PoCValidatePhase, testModelA))

	body, err := json.Marshal(map[string]any{
		"block_hash":          "abc",
		"block_height":        100,
		"public_key":          "02b463f7f42e5f4f1d2d0bb1c4b9f8d2c3b1a09c72fbc5d0b8d4c53b37f6f2a540",
		"node_id":             1,
		"n_total":             4,
		"n_mismatch":          2,
		"mismatch_nonces":     []int{0, 1},
		"p_value":             1.0,
		"fraud_detected":      false,
		"insufficient_sample": true,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v2/poc-batches/model-a/validated", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	mockRecorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
	mockRecorder.AssertNotCalled(t, "SubmitPoCChallengeValidations", mock.Anything)
}
