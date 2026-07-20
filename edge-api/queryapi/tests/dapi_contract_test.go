package queryapitest

import (
	"encoding/json"
	"net/http"
	"sort"
	"testing"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dapiTopLevelKeys documents JSON top-level keys returned by decentralized-api
// public handlers for read-only query routes. edge-api must expose the same keys
// so existing clients keep working when the proxy steers traffic to edge-api.
var dapiTopLevelKeys = map[string][]string{
	"GET /v1/status":                          {"status"},
	"GET /v1/models":                          {"object", "data"},
	"GET /v1/governance/models":               {"models"},
	"GET /v1/governance/models-legacy":        {"model"},
	"GET /v1/pricing":                         {"unit_of_compute_price", "models"},
	"GET /v1/versions":                          {"api_version", "node_version", "timestamp"},
	"GET /v1/epochs/{epoch}/participants":     {"active_participants", "addresses", "active_participants_bytes", "proof_ops", "validators", "block", "excluded_participants"},
}

func TestResponseTopLevelKeysMatchDapiContract(t *testing.T) {
	cases := []struct {
		name   string
		keys   []string
		run    func(t *testing.T) (int, []byte)
	}{
		{
			name: "GET /v1/status",
			keys: dapiTopLevelKeys["GET /v1/status"],
			run: func(t *testing.T) (int, []byte) {
				h := newHandlers(&fakeChain{})
				ctx, rec := echoContext(t, http.MethodGet, "/v1/status")
				require.NoError(t, h.GetStatus(ctx))
				return rec.Code, rec.Body.Bytes()
			},
		},
		{
			name: "GET /v1/models",
			keys: dapiTopLevelKeys["GET /v1/models"],
			run: func(t *testing.T) (int, []byte) {
				model := makeModel("contract-model", 3)
				srv := &stubModelsServer{
					epochGroupData: inferencetypes.EpochGroupData{
						EpochIndex:     1,
						SubGroupModels: []string{"contract-model"},
					},
					subGroupData: map[string]inferencetypes.EpochGroupData{
						"contract-model": {ModelSnapshot: &model},
					},
				}
				h := handlersWithInference(t, srv)
				ctx, rec := echoContext(t, http.MethodGet, "/v1/models")
				require.NoError(t, h.GetModels(ctx))
				return rec.Code, rec.Body.Bytes()
			},
		},
		{
			name: "GET /v1/governance/models",
			keys: dapiTopLevelKeys["GET /v1/governance/models"],
			run: func(t *testing.T) (int, []byte) {
				srv := &stubGovernanceModelsServer{models: []inferencetypes.Model{makeModel("gov", 1)}}
				h := handlersWithInference(t, srv)
				ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/models")
				require.NoError(t, h.GetGovernanceModels(ctx))
				return rec.Code, rec.Body.Bytes()
			},
		},
		{
			name: "GET /v1/governance/models-legacy",
			keys: dapiTopLevelKeys["GET /v1/governance/models-legacy"],
			run: func(t *testing.T) (int, []byte) {
				srv := &stubGovernanceModelsServer{models: []inferencetypes.Model{makeModel("legacy", 1)}}
				h := handlersWithInference(t, srv)
				ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/models/legacy")
				require.NoError(t, h.GetGovernanceModelsLegacy(ctx))
				return rec.Code, rec.Body.Bytes()
			},
		},
		{
			name: "GET /v1/pricing",
			keys: dapiTopLevelKeys["GET /v1/pricing"],
			run: func(t *testing.T) (int, []byte) {
				model := makeModel("priced", 2)
				srv := &stubPricingServer{
					epochGroupData: inferencetypes.EpochGroupData{
						EpochIndex:         1,
						UnitOfComputePrice: 7,
						SubGroupModels:     []string{"priced"},
					},
					subGroupData: map[string]inferencetypes.EpochGroupData{
						"priced": {ModelSnapshot: &model},
					},
				}
				h := handlersWithInference(t, srv)
				ctx, rec := echoContext(t, http.MethodGet, "/v1/pricing")
				require.NoError(t, h.GetPricing(ctx))
				return rec.Code, rec.Body.Bytes()
			},
		},
		{
			name: "GET /v1/versions",
			keys: dapiTopLevelKeys["GET /v1/versions"],
			run: func(t *testing.T) (int, []byte) {
				h := handlersWithComet(t, &stubNodeInfoServer{})
				ctx, rec := echoContext(t, http.MethodGet, "/v1/versions")
				require.NoError(t, h.GetVersions(ctx))
				return rec.Code, rec.Body.Bytes()
			},
		},
		{
			name: "GET /v1/epochs/{epoch}/participants",
			keys: dapiTopLevelKeys["GET /v1/epochs/{epoch}/participants"],
			run: func(t *testing.T) (int, []byte) {
				h := handlersWithInferenceAndComet(t, &stubEpochParticipantsInference{}, &stubEpochParticipantsComet{})
				ctx, rec := echoContext(t, http.MethodGet, "/v1/epochs/1/participants")
				require.NoError(t, h.GetEpochParticipants(ctx, "1"))
				return rec.Code, rec.Body.Bytes()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := tc.run(t)
			require.Equal(t, http.StatusOK, code, "body=%s", string(body))
			assertDapiKeysPresent(t, tc.keys, topLevelJSONKeys(t, body))
		})
	}
}

func assertDapiKeysPresent(t *testing.T, required, got []string) {
	t.Helper()
	gotSet := make(map[string]struct{}, len(got))
	for _, k := range got {
		gotSet[k] = struct{}{}
	}
	for _, k := range required {
		_, ok := gotSet[k]
		assert.True(t, ok, "missing dapi contract key %q in response keys %v", k, got)
	}
}

func TestDapiCompatJSONKeysHelper(t *testing.T) {
	// Regression guard for the live-server compatibility harness: when bodies differ
	// only in dynamic values, top-level keys must still match.
	a := `{"status":"ok","block_height":1}`
	b := `{"status":"ok","block_height":2}`
	assert.Equal(t, sortedStrings([]string{"block_height", "status"}), sortedStrings(jsonKeysOnly(a)))
	assert.Equal(t, sortedStrings(jsonKeysOnly(a)), sortedStrings(jsonKeysOnly(b)))
}

func topLevelJSONKeys(t *testing.T, body []byte) []string {
	t.Helper()
	return jsonKeysOnly(string(body))
}

func jsonKeysOnly(raw string) []string {
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	return keys
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
