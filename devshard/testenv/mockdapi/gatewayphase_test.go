package mockdapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func getGatewayJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

// Test flow:
// 1. Start mock-dapi against an in-process mock-chain and read /v1/epochs/latest.
// 2. Advance the mock-chain epoch through mock-dapi's /testenv/epoch proxy.
// 3. Require /v1/epochs/latest to report the new epoch index.
// 4. POST a PoCGenerate step to /testenv/phase and require the phase to change.
// 5. Require /v1/epochs/current/participants to list the seeded mock-chain participant.
func TestMockDAPI_GatewayPhaseFollowsChain(t *testing.T) {
	bed := startBed(t)
	t.Cleanup(bed.cleanup)

	before := getGatewayJSON(t, bed.httpURL+"/v1/epochs/latest")
	require.Equal(t, "1", before["latest_epoch"].(map[string]any)["index"])

	resp, err := http.Post(bed.httpURL+"/testenv/epoch", "application/json", strings.NewReader(`{"advance":true}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	after := getGatewayJSON(t, bed.httpURL+"/v1/epochs/latest")
	require.Equal(t, "2", after["latest_epoch"].(map[string]any)["index"])
	require.Equal(t, "Inference", after["phase"])

	resp, err = http.Post(bed.httpURL+"/testenv/phase", "application/json", strings.NewReader(`{"steps":[{"at":"0s","phase":"PoCGenerate"}]}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "PoCGenerate", getGatewayJSON(t, bed.httpURL+"/v1/epochs/latest")["phase"])

	participants := getGatewayJSON(t, bed.httpURL+"/v1/epochs/current/participants")["active_participants"].(map[string]any)["participants"].([]any)
	require.Len(t, participants, 1)
	require.Equal(t, "gonka1host000000000000000000000000000000000", participants[0].(map[string]any)["index"])
}
