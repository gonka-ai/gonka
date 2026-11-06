package loadtest

import (
	"context"
	"devshard/testenv/config"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow: accept both inference wire formats without losing the start time
// used to judge stuck work; reject duplicate nonces rather than hide records.
func TestStandaloneInferenceWireFormat(t *testing.T) {
	for _, raw := range []string{
		`{"inferences":{"7":{"status":"started","started_at":123,"executor_slot":2}}}`,
		`{"inferences":[{"nonce":7,"status":"started","record":{"status":1,"started_at":123,"executor_slot":2}}]}`,
	} {
		var body debugInferencesResponse
		require.NoError(t, json.Unmarshal([]byte(raw), &body))
		require.Equal(t, int64(123), body.Inferences["7"].StartedAt)
		require.Equal(t, "started", body.Inferences["7"].Status)
		require.Equal(t, uint32(2), body.Inferences["7"].ExecutorSlot)
	}
	var body debugInferencesResponse
	require.ErrorContains(t, json.Unmarshal([]byte(`{"inferences":[{"nonce":7,"record":{}},{"nonce":7,"record":{}}]}`), &body), "duplicate")
}

// Test flow: the standalone API rejects legacy nested settings. Assert the
// runner sends an explicit rotation disable and a native engine timing.
func TestStandaloneGatewaySettings(t *testing.T) {
	s, err := LoadScenario(filepath.Join("scenarios", "poc-gateway-v7.yaml"))
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/admin/settings", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, map[string]any{"rotation_enabled": false, "engine_loser_grace_ms": float64(30000)}, body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	require.NoError(t, applyGatewayScenarioSettings(context.Background(), server.URL, "test-key", s, nil))
	s.Gateway.Redundancy.SecondaryWaitAfterWinner = "30s"
	require.ErrorContains(t, s.Validate(), "legacy redundancy")
}

// Test flow: selected PoC checks cannot omit PoC evaluation or name a typo.
// Non-selected legacy checks stay out of the verdict and are listed in scope.
func TestStandalonePoCInvariantScope(t *testing.T) {
	s, err := LoadScenario(filepath.Join("scenarios", "poc-gateway-v7.yaml"))
	require.NoError(t, err)
	require.Equal(t, 8*time.Minute, s.Duration())
	require.Equal(t, "cpoc", s.Workload.Events[1].Params["kind"])
	settings := resolveInvariantSettings(s)
	settings.Checks = []string{"poc-load"}
	checks, report := evaluateInvariants(InvariantInputs{Settings: settings})
	require.Empty(t, checks)
	require.Contains(t, report.Tables, "scope")
	s.Assertions.Invariants.Checks = []string{"unknown"}
	require.ErrorContains(t, s.Validate(), "unknown")
	s.Assertions.Invariants.Checks = []string{"faults-fired-and-ml-nodes-bound"}
	require.ErrorContains(t, s.Validate(), "poc-load")
}

// Test flow: a fresh checkout can configure the standalone image without a
// generated base config, but a mismatched protocol must fail before startup.
func TestStandaloneRunnerConfiguration(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "poc-gateway-v7.yaml"))
	require.NoError(t, err)
	workDir := t.TempDir()
	profiles := map[string]Profile{"prod-like": {TTFT: "1ms", TokenInterval: "1ms", Workers: 1}}
	require.NoError(t, writeRunnerConfig(t.TempDir(), workDir, scenario, profiles, false, nil, nil))
	cfg, err := config.Load(filepath.Join(workDir, "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, "gateway", cfg.Devshardctl.Binary)
	require.Equal(t, "local/gateway:46ce696a-v2", cfg.Devshardctl.Image)
	require.Equal(t, "v2", cfg.Versiond.VersionName)
	scenario.Gateway.Protocol = "v6"
	require.ErrorContains(t, writeRunnerConfig(t.TempDir(), t.TempDir(), scenario, profiles, false, nil, nil), "does not match host protocol")
}

// The CLI image wins over a manifest, the manifest wins over YAML, and neither
// the source scenario nor the caller's build definition may be mutated.
func TestGatewayImageOverridePrecedenceAndSavedScenario(t *testing.T) {
	source := filepath.Join("scenarios", "poc-gateway-v7.yaml")
	scenario, err := LoadScenario(source)
	require.NoError(t, err)
	original := scenario.Gateway.Image
	build := &Build{Images: BuildImages{Gateway: "local/gateway:manifest"}}
	for _, tc := range []struct {
		image string
		want  string
	}{
		{"", "local/gateway:manifest"}, {"local/gateway:cli", "local/gateway:cli"},
	} {
		effective, chosen, err := resolveGatewayImage(scenario, build, tc.image)
		require.NoError(t, err)
		require.Equal(t, tc.want, effective.Gateway.Image)
		require.Equal(t, tc.want, chosen.Images.Gateway)
		require.Equal(t, "local/gateway:manifest", build.Images.Gateway)
		require.Equal(t, original, scenario.Gateway.Image)
		saved := filepath.Join(t.TempDir(), "run.yaml")
		require.NoError(t, saveRunScenario(source, saved, effective))
		restored, err := LoadScenario(saved)
		require.NoError(t, err)
		require.Equal(t, tc.want, restored.Gateway.Image)
		require.Equal(t, scenario.Workload.Events, restored.Workload.Events)
	}
	unchanged, _, err := resolveGatewayImage(scenario, nil, "")
	require.NoError(t, err)
	require.Equal(t, original, unchanged.Gateway.Image)
	legacy := testScenario()
	_, _, err = resolveGatewayImage(legacy, nil, "local/gateway:new")
	require.ErrorContains(t, err, "requires gateway.implementation")
}
