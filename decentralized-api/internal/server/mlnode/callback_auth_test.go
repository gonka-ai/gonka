package mlnode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/callbackauth"
	"decentralized-api/mlnodeclient"
	"decentralized-api/poc"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const otherValidatedPubKey = "03b463f7f42e5f4f1d2d0bb1c4b9f8d2c3b1a09c72fbc5d0b8d4c53b37f6f2a540"

func validatedBody(t *testing.T, blockHeight int64, pubKey string, nTotal int64, extra map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"block_hash":      "abc",
		"block_height":    blockHeight,
		"public_key":      pubKey,
		"node_id":         1,
		"n_total":         nTotal,
		"n_mismatch":      0,
		"mismatch_nonces": []int{},
		"p_value":         1.0,
		"fraud_detected":  false,
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return raw
}

func postJSON(server *Server, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.e.ServeHTTP(rec, req)
	return rec
}

func newValidateServer(t *testing.T, recorder *cosmosclient.MockCosmosMessageClient, opts ...ServerOption) *Server {
	t.Helper()
	poc.OpenChallenges.Reset()
	t.Cleanup(poc.OpenChallenges.Reset)
	opts = append([]ServerOption{WithCallbackSigner(testCallbackSigner)}, opts...)
	return NewServer(recorder, newMLNodeTestBroker(t, types.PoCValidatePhase, testModelA), opts...)
}

func expectValidation(recorder *cosmosclient.MockCosmosMessageClient, weightPositive bool) {
	recorder.
		On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
			return msg != nil && len(msg.Validations) == 1 &&
				(msg.Validations[0].ValidatedWeight > 0) == weightPositive
		})).
		Return(nil).
		Once()
}

func TestValidatedCallbackRejectsForgedRequests(t *testing.T) {
	foreign, err := callbackauth.NewSigner(bytes.Repeat([]byte{9}, callbackauth.SecretSize))
	require.NoError(t, err)
	foreignPath := "/v2/poc-batches/" + callbackModelSegment(testModelA) +
		callbackauth.ValidationPath(5, 0, foreign.ValidationToken(100, testModelA, testValidatedPubKey, 5, 0)) + "/validated"

	cases := []struct {
		name string
		path string
		body []byte
		code int
	}{
		{"legacy route", "/v2/poc-batches/model-a/validated", validatedBody(t, 100, testValidatedPubKey, 5, nil), http.StatusNotFound},
		{"made-up token", "/v2/poc-batches/model-a/v/5/0/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/validated", validatedBody(t, 100, testValidatedPubKey, 5, nil), http.StatusForbidden},
		{"route without min count", strings.Replace(validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), "/v/5/0/", "/v/5/", 1), validatedBody(t, 100, testValidatedPubKey, 5, nil), http.StatusNotFound},
		{"lowered min count", strings.Replace(validatedCallbackPathWithMin(testModelA, 100, testValidatedPubKey, 200, 200), "/v/200/200/", "/v/200/0/", 1), validatedBody(t, 100, testValidatedPubKey, 199, nil), http.StatusForbidden},
		{"invalid min count", strings.Replace(validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), "/v/5/0/", "/v/5/-1/", 1), validatedBody(t, 100, testValidatedPubKey, 5, nil), http.StatusBadRequest},
		{"other public key", validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), validatedBody(t, 100, otherValidatedPubKey, 5, nil), http.StatusForbidden},
		{"other block height", validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), validatedBody(t, 101, testValidatedPubKey, 5, nil), http.StatusForbidden},
		{"other model", strings.Replace(validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), callbackModelSegment(testModelA), callbackModelSegment(testModelB), 1), validatedBody(t, 100, testValidatedPubKey, 5, nil), http.StatusForbidden},
		{"other sample size", strings.Replace(validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), "/v/5/", "/v/6/", 1), validatedBody(t, 100, testValidatedPubKey, 5, nil), http.StatusForbidden},
		{"foreign secret", foreignPath, validatedBody(t, 100, testValidatedPubKey, 5, nil), http.StatusForbidden},
		{"invalid sample size", strings.Replace(validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), "/v/5/", "/v/x/", 1), validatedBody(t, 100, testValidatedPubKey, 5, nil), http.StatusBadRequest},
		{"n_total above dispatched sample", validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), validatedBody(t, 100, testValidatedPubKey, 6, nil), http.StatusUnprocessableEntity},
		{"zero n_total", validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), validatedBody(t, 100, testValidatedPubKey, 0, nil), http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &cosmosclient.MockCosmosMessageClient{}
			server := newValidateServer(t, recorder)
			rec := postJSON(server, tc.path, tc.body)
			require.Equal(t, tc.code, rec.Code, rec.Body.String())
			recorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
			recorder.AssertNotCalled(t, "SubmitPoCChallengeValidations", mock.Anything)
		})
	}
}

func TestValidatedCallbackAcceptsGenuineVerdicts(t *testing.T) {
	cases := []struct {
		name     string
		pubKey   string
		nTotal   int64
		extra    map[string]any
		positive bool
	}{
		{"full sample", testValidatedPubKey, 5, nil, true},
		{"validator dropped nonces", testValidatedPubKey, 3, nil, true},
		{"upper-case public key", strings.ToUpper(testValidatedPubKey), 5, nil, true},
		{"fraud", testValidatedPubKey, 5, map[string]any{"fraud_detected": true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &cosmosclient.MockCosmosMessageClient{}
			expectValidation(recorder, tc.positive)
			server := newValidateServer(t, recorder)
			rec := postJSON(server, validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), validatedBody(t, 100, tc.pubKey, tc.nTotal, tc.extra))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			recorder.AssertExpectations(t)
		})
	}
}

func TestValidatedCallbackAbstains(t *testing.T) {
	cases := []struct {
		name     string
		sampled  int64
		minCount uint32
		nTotal   int64
		extra    map[string]any
	}{
		{"insufficient sample reported", 5, 0, 5, map[string]any{"insufficient_sample": true}},
		{"validator lost nonces below dispatched min_count", 200, 200, 199, nil},
		{"dispatched min_count above sample", 5, 200, 5, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &cosmosclient.MockCosmosMessageClient{}
			server := newValidateServer(t, recorder)
			path := validatedCallbackPathWithMin(testModelA, 100, testValidatedPubKey, tc.sampled, tc.minCount)
			rec := postJSON(server, path, validatedBody(t, 100, testValidatedPubKey, tc.nTotal, tc.extra))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			recorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
		})
	}
}

func TestValidatedCallbackMinCountComesFromDispatchNotCache(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	expectValidation(recorder, true)
	server := newValidateServer(t, recorder)
	path := validatedCallbackPathWithMin(testModelA, 100, testValidatedPubKey, 200, 200)

	rec := postJSON(server, path, validatedBody(t, 100, testValidatedPubKey, 200, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	recorder.AssertExpectations(t)
}

func TestGeneratedCallbackRejectsForgedRequests(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"block_hash":   "abc",
		"block_height": 100,
		"public_key":   "pub",
		"node_id":      1,
		"artifacts": []map[string]any{
			{"nonce": 1, "vector_b64": base64.StdEncoding.EncodeToString([]byte{1, 2, 3})},
		},
	})
	require.NoError(t, err)

	cases := []struct {
		name string
		path string
		code int
	}{
		{"legacy route", "/v2/poc-batches/model-a/generated", http.StatusNotFound},
		{"legacy per-model route", "/v2/poc-batches/model-a/m/generated", http.StatusNotFound},
		{"made-up token", "/v2/poc-batches/model-a/g/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/generated", http.StatusForbidden},
		{"other stage", generatedCallbackPath(testModelA, 99, false, 1), http.StatusForbidden},
		{"other node", generatedCallbackPath(testModelA, 100, false, 2), http.StatusForbidden},
		{"other model", strings.Replace(generatedCallbackPath(testModelB, 100, false, 1), callbackModelSegment(testModelB), callbackModelSegment(testModelA), 1), http.StatusForbidden},
		{"global token on per-model route", strings.Replace(generatedCallbackPath(testModelA, 100, false, 1), "/generated", "/m/generated", 1), http.StatusForbidden},
		{"per-model token on global route", strings.Replace(generatedCallbackPath(testModelA, 100, true, 1), "/m/generated", "/generated", 1), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := artifacts.NewManagedArtifactStore(t.TempDir(), 3)
			defer store.Close()
			server := NewServer(nil, newMLNodeTestBroker(t, types.PoCGeneratePhase, testModelA, testModelB), WithArtifactStore(store), WithCallbackSigner(testCallbackSigner))

			rec := postJSON(server, tc.path, body)
			require.Equal(t, tc.code, rec.Code, rec.Body.String())
			_, err := store.GetStore(100, testModelA)
			require.Error(t, err)
		})
	}
}

func TestCallbackRoutesRejectWithoutSigner(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	poc.OpenChallenges.Reset()
	t.Cleanup(poc.OpenChallenges.Reset)
	server := NewServer(recorder, newMLNodeTestBroker(t, types.PoCValidatePhase, testModelA))

	rec := postJSON(server, validatedCallbackPath(testModelA, 100, testValidatedPubKey, 5), validatedBody(t, 100, testValidatedPubKey, 5, nil))
	require.Equal(t, http.StatusForbidden, rec.Code)
	recorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
}

func TestGeneratedCallbackRoundTripFromBrokerCommand(t *testing.T) {
	modelIDs := []string{testModelA, testModelB, ".", "..", "g", "v", "with space", "模型/雪"}
	testBroker := newMLNodeTestBroker(t, types.PoCGeneratePhase, modelIDs...)
	nodes, err := testBroker.GetNodes()
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	nodeState := &broker.NodeWithState{Node: nodes[0].Node, State: nodes[0].State}

	store := artifacts.NewManagedArtifactStore(t.TempDir(), 3)
	defer store.Close()
	server := NewServer(nil, testBroker, WithArtifactStore(store), WithCallbackSigner(testCallbackSigner))

	for _, modelID := range modelIDs {
		client := mlnodeclient.NewMockClient()
		worker := broker.NewNodeWorkerWithClient(nodeState.Node.Id, nodeState, client, testBroker)
		cmd := broker.StartPoCNodeCommandV2{
			BlockHeight:    100,
			BlockHash:      "abc",
			PubKey:         "pub",
			CallbackUrl:    broker.GetPoCCallbackBaseURLV2("http://callback"),
			CallbackSigner: testCallbackSigner,
			TotalNodes:     1,
			Model:          modelID,
			SeqLen:         256,
		}
		require.True(t, cmd.Execute(context.Background(), worker).Succeeded)
		worker.Shutdown()

		client.Mu.Lock()
		initReq := *client.LastInitGenerateV2Req
		client.Mu.Unlock()
		require.True(t, strings.HasPrefix(initReq.URL, "http://callback"))

		body, err := json.Marshal(map[string]any{
			"block_hash":   "abc",
			"block_height": 100,
			"public_key":   "pub",
			"node_id":      initReq.NodeId,
			"artifacts": []map[string]any{
				{"nonce": 1, "vector_b64": base64.StdEncoding.EncodeToString([]byte{1, 2, 3})},
			},
		})
		require.NoError(t, err)
		callbackURL, err := url.JoinPath(initReq.URL, "generated")
		require.NoError(t, err)
		rec := postJSON(server, strings.TrimPrefix(callbackURL, "http://callback"), body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		modelStore, err := store.GetStore(100, modelID)
		require.NoError(t, err)
		require.Equal(t, uint32(1), modelStore.Count())
	}
}
