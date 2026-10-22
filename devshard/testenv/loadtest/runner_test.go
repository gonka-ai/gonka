package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"
	"github.com/stretchr/testify/require"
)

func TestWriteComposeLogsTail(t *testing.T) {
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf 'stderr line\\n' >&2\n"), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tail := range []string{"5000", "100", "all"} {
		t.Run(tail, func(t *testing.T) {
			outputDir := t.TempDir()
			require.NoError(t, writeComposeLogs(outputDir, outputDir, "project", "compose.yaml", tail))
			require.NoError(t, writeComposeLogsTo(outputDir, outputDir, "project", "compose.yaml", "compose-pre-drain.log", tail))
			for _, filename := range []string{"compose.log", "compose-pre-drain.log"} {
				body, err := os.ReadFile(filepath.Join(outputDir, filename))
				require.NoError(t, err)
				require.Equal(t, "compose\n-p\nproject\n-f\ncompose.yaml\nlogs\n--no-color\n--tail\n"+tail+"\nstderr line\n", string(body))
			}
		})
	}
}

func TestConfigureParticipants_ThreeParticipants(t *testing.T) {
	cfg := &config.File{Hosts: []config.HostCfg{
		{ID: "versiond-0"},
		{ID: "versiond-1"},
		{ID: "versiond-2"},
	}}

	configureParticipants(cfg, 3)

	require.Equal(t, 3, cfg.Escrow.Slots)
	require.Len(t, cfg.Hosts, 4)
	require.Equal(t, "versiond-0", cfg.Hosts[0].KeyName)
	require.Equal(t, "versiond-0", cfg.Hosts[1].KeyName)
	require.Equal(t, "versiond-2", cfg.Hosts[2].KeyName)
	require.Equal(t, "versiond-3", cfg.Hosts[3].KeyName)
}

func TestConfigureReplayModels(t *testing.T) {
	cfg := &config.File{
		Epoch: config.Epoch{Index: 1},
		Escrows: []config.Escrow{{
			ID:     7,
			Amount: 123,
			Slots:  []string{"participant"},
		}},
		EpochGroups: []config.EpochGroupBinding{{
			EpochIndex:          1,
			ValidationThreshold: 95,
			ValidationExponent:  -2,
		}},
	}

	require.NoError(t, configureReplayModels(cfg, []string{"model-a", "model-b"}))
	require.Len(t, cfg.Escrows, 2)
	require.Equal(t, uint64(7), cfg.Escrows[0].ID)
	require.Equal(t, uint64(8), cfg.Escrows[1].ID)
	require.Equal(t, []string{"model-a", "model-b"}, []string{cfg.Escrows[0].ModelID, cfg.Escrows[1].ModelID})
	require.Equal(t, []string{"participant"}, cfg.Escrows[1].Slots)
	require.Equal(t, []string{"model-a", "model-b"}, []string{cfg.EpochGroups[0].ModelID, cfg.EpochGroups[1].ModelID})
}

func TestIsAddressInUse(t *testing.T) {
	require.True(t, isAddressInUse(errors.New("failed to set up container networking: Address already in use")))
	require.False(t, isAddressInUse(errors.New("container is unhealthy")))
	require.False(t, isAddressInUse(nil))
}

func TestLoadTestComposeEnvEnablesH2(t *testing.T) {
	t.Setenv("DEVSHARD_RPC_H2_UPGRADE", "false")
	t.Setenv("DEVSHARD_RPC_GRPC", "false")

	env := loadTestComposeEnv()
	require.Empty(t, testEnvValue(env, "DEVSHARD_RPC_H2_UPGRADE"), "the HTTP/2 default must not be forced off")
	require.Equal(t, "true", testEnvValue(env, "DEVSHARD_RPC_GRPC"))
}

func testEnvValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func TestStripStaticNetworking(t *testing.T) {
	compose := `networks:
  testenv:
    driver: bridge
    ipam:
      config:
        - subnet: 172.31.42.0/24
services:
  mock-chain:
    networks:
      testenv:
        ipv4_address: 172.31.42.2
`

	actual := stripStaticNetworking(compose)
	require.NotContains(t, actual, "ipam:")
	require.NotContains(t, actual, "subnet:")
	require.NotContains(t, actual, "ipv4_address:")
	require.Contains(t, actual, "driver: bridge")
}

func TestFormatStatusCounts(t *testing.T) {
	require.Equal(t, "finished=3, started=1", formatStatusCounts(map[string]int{
		"started":  1,
		"finished": 3,
	}))
	require.Equal(t, "unavailable", formatStatusCounts(nil))
}

func TestAssertRunReportsExpectedAndActualValues(t *testing.T) {
	scenario := testScenario()
	summary := Summary{
		Requests:  4,
		Completed: 2,
		Failed:    2,
		Dropped:   1,
		ErrorRate: 0.5,
	}

	_, checks, err := assertRun(context.Background(), scenario, summary, nil, "", "", nil, nil)

	require.Error(t, err)
	require.Len(t, checks, 1)
	require.Equal(t, "requests.error_rate", checks[0].Name)
	require.False(t, checks[0].Passed)
	require.Equal(t, "<= 0.0000 (0.00%)", checks[0].Expected)
	require.Equal(t, "0.5000 (50.00%)", checks[0].Actual)
	require.Contains(t, checks[0].Details, "http_failures=1")
	require.Contains(t, err.Error(), "expected")
	require.Contains(t, err.Error(), "actual")
}

func TestAssertRunEvaluatesAllEnabledAssertions(t *testing.T) {
	scenario := testScenario()
	scenario.Assertions.MockML.RequireEachNodeUsed = true
	summary := Summary{
		Requests:  4,
		Completed: 2,
		Failed:    2,
		ErrorRate: 0.5,
	}

	_, checks, err := assertRun(context.Background(), scenario, summary, nil, "", "", nil, nil)

	require.Error(t, err)
	require.Len(t, checks, 3)
	require.Equal(t, []string{
		"requests.error_rate",
		"mock_ml.node_used.mock-openai-0",
		"mock_ml.node_used.mock-openai-1",
	}, []string{checks[0].Name, checks[1].Name, checks[2].Name})
	require.Contains(t, err.Error(), "3 assertions failed")
}

func TestWaitForNoOrphanedWork_AllowsLoggedGhostPendingWithinThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"finished"},"2":{"status":"pending"}}}`)
	}))
	defer server.Close()

	summary, err := waitForNoOrphanedWork(context.Background(), server.URL, "", 0.5, map[string]struct{}{"2": {}}, time.Second)
	require.NoError(t, err)
	require.Equal(t, 1, summary.Finished)
	require.Zero(t, summary.Orphaned)
}

func TestWaitForNoOrphanedWork_RejectsGhostRateAboveThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"finished"},"2":{"status":"pending"}}}`)
	}))
	defer server.Close()

	_, err := waitForNoOrphanedWork(context.Background(), server.URL, "", 0.25, map[string]struct{}{"2": {}}, time.Second)
	require.ErrorContains(t, err, "ghost inference rate 0.5000")
}

func TestWaitForNoOrphanedWorkDoesNotRequireOneInferencePerRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"finished"}}}`)
	}))
	defer server.Close()

	summary, err := waitForNoOrphanedWork(context.Background(), server.URL, "", 0, nil, time.Second)
	require.NoError(t, err)
	require.Equal(t, 1, summary.Total)
	require.Equal(t, 1, summary.Finished)
}

func TestWaitForNoOrphanedWorkRejectsPendingInference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"pending"}}}`)
	}))
	defer server.Close()

	_, err := waitForNoOrphanedWork(context.Background(), server.URL, "", 0, nil, 10*time.Millisecond)
	require.ErrorContains(t, err, "orphaned=1")
	require.ErrorContains(t, err, "pending=1")
}

func TestWaitForNoOrphanedWorkAcceptsTerminalStatuses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"validated"},"2":{"status":"invalidated"},"3":{"status":"timed_out"}}}`)
	}))
	defer server.Close()

	summary, err := waitForNoOrphanedWork(context.Background(), server.URL, "", 0, nil, time.Second)
	require.NoError(t, err)
	require.Zero(t, summary.Orphaned)
	require.Equal(t, 3, summary.Total)
}

func TestReadGhostInferenceIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.log")
	require.NoError(t, os.WriteFile(path, []byte("devshardctl | request=r1 stage=ghost_probe_skipped escrow=1 nonce=9 reason=no_compatible_request_after_stale\n"), 0o644))

	ghosts, err := readGhostInferenceIDs(path)
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{"9": {}}, ghosts)
}

func TestReadGhostInferenceIDsJSONLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.log")
	log := `devshardctl-1 | {"time":"2026-10-14T12:28:14Z","level":"INFO","msg":"ghost_probe_skipped","stage":"ghost_probe_skipped","escrow":"1","nonce":271,"reason":"participant_catching_up_no_send"}` + "\n" +
		`devshardctl-1 | {"time":"2026-10-14T12:28:14Z","level":"INFO","msg":"started","stage":"started","escrow":"1","nonce":272}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(log), 0o644))

	ghosts, err := readGhostInferenceIDs(path)
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{"271": {}}, ghosts)
}

func TestFetchGatewayStateSizes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/debug/state-sizes", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		_, _ = fmt.Fprint(w, `{"diffs":12,"diffs_bytes":12345,"diffs_mb":0.011,"signature_nonces":8,"nonce_states":14,"pending_txs":2,"applied_tx_keys":11}`)
	}))
	defer server.Close()

	sizes, err := fetchGatewayStateSizes(context.Background(), server.URL, "test-key")
	require.NoError(t, err)
	require.Equal(t, GatewayStateSizes{
		Diffs:           12,
		DiffsBytes:      12345,
		DiffsMB:         0.011,
		SignatureNonces: 8,
		NonceStates:     14,
		PendingTxs:      2,
		AppliedTxKeys:   11,
	}, sizes)
}

func TestScopedGatewayURL(t *testing.T) {
	require.Equal(t, "http://127.0.0.1:1234/devshard/7", scopedGatewayURL("http://127.0.0.1:1234/", "7"))
	require.Equal(t, "http://127.0.0.1:1234", scopedGatewayURL("http://127.0.0.1:1234/", ""))
}

func TestRotationModelIDsPreferDatasetModels(t *testing.T) {
	scenario := testScenario()
	scenario.Gateway.EscrowRotation.Enabled = true
	cfg := &config.File{
		Escrows: []config.Escrow{{ModelID: "config-model"}},
	}
	got := rotationModelIDs(cfg, []string{"dataset-model", "dataset-model"}, scenario)
	require.Equal(t, []string{"dataset-model", "config-model", scenario.Workload.Request.Model}, got)
}

func TestWriteGatewayStateSizes(t *testing.T) {
	outputDir := t.TempDir()
	require.NoError(t, writeGatewayStateSizes(outputDir, GatewayStateSizes{
		Diffs:           12,
		DiffsBytes:      12345,
		DiffsMB:         0.011,
		SignatureNonces: 8,
		NonceStates:     14,
		PendingTxs:      2,
		AppliedTxKeys:   11,
	}))

	body, err := os.ReadFile(filepath.Join(outputDir, "gateway-state.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"diffs":12,"diffs_bytes":12345,"diffs_mb":0.011,"signature_nonces":8,"nonce_states":14,"pending_txs":2,"applied_tx_keys":11}`, string(body))
}

func TestDisabledRotationDoesNotConfigureGatewayNonceLimit(t *testing.T) {
	scenario := testScenario()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.NotContains(t, body, "escrow_rotation")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	require.NoError(t, applyGatewayScenarioSettings(context.Background(), server.URL, "", scenario, nil))
}

func TestApplyGatewayParticipantQuarantine(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings GatewayParticipantThrottle
		want     map[string]int64
	}{
		{name: "defaults"},
		{name: "transport only", settings: GatewayParticipantThrottle{TransportFailureQuarantine: "30s"}, want: map[string]int64{"transport_failure_quarantine_ms": 30000}},
		{name: "http only", settings: GatewayParticipantThrottle{HTTPQuarantine: "45s"}, want: map[string]int64{"http_quarantine_ms": 45000}},
		{name: "both", settings: GatewayParticipantThrottle{TransportFailureQuarantine: "70s", HTTPQuarantine: "70s"}, want: map[string]int64{"transport_failure_quarantine_ms": 70000, "http_quarantine_ms": 70000}},
		{name: "empty stream", settings: GatewayParticipantThrottle{EmptyStreamQuarantine: "1s"}, want: map[string]int64{"empty_stream_quarantine_ms": 1000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scenario := testScenario()
			scenario.Gateway.ParticipantThrottle = tc.settings
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/admin/settings", r.URL.Path)
				var body map[string]json.RawMessage
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				if tc.want == nil {
					require.NotContains(t, body, "participant_throttle")
				} else {
					var throttle map[string]int64
					require.NoError(t, json.Unmarshal(body["participant_throttle"], &throttle))
					require.Equal(t, tc.want, throttle)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			require.NoError(t, applyGatewayScenarioSettings(context.Background(), server.URL, "", scenario, nil))
		})
	}
}

// testenvWithBaseConfig is a testenv dir whose config/config.yaml is empty, so the runner starts
// from the config defaults; the real file is written per checkout and not tracked.
func testenvWithBaseConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "config.yaml"), []byte("{}\n"), 0o644))
	return dir
}

// Test flow:
// 1. Load both dishonest-executor scenarios, which ask for mainnet's 0.958 similarity bar.
// 2. Write the runner config from a testenv dir with no config/config.yaml.
// 3. Check every epoch group carries 958e-3, not the 0.50 default no logprob mismatch can fail.
func TestWriteRunnerConfigCarriesTheScenarioValidationThreshold(t *testing.T) {
	for _, name := range []string{"dishonest-executor-wrong-answer", "dishonest-executor-overclaim"} {
		scenario, err := LoadScenario(filepath.Join("scenarios", name+".yaml"))
		require.NoError(t, err, name)
		require.NotNil(t, scenario.Topology.Chain.ValidationThreshold, name)
		profiles := make(map[string]Profile)
		for _, node := range scenario.Topology.MockML.Nodes {
			profile, err := LoadProfile("profiles", node.Profile)
			require.NoError(t, err, name)
			profiles[node.Profile] = profile
		}
		workDir := t.TempDir()
		require.NoError(t, writeRunnerConfig(testenvWithBaseConfig(t), workDir, scenario, profiles, false, nil), name)
		cfg, err := config.Load(filepath.Join(workDir, "config.yaml"))
		require.NoError(t, err, name)
		require.NotEmpty(t, cfg.EpochGroups, name)
		for _, group := range cfg.EpochGroups {
			require.Equal(t, int64(958), group.ValidationThreshold, name)
			require.Equal(t, int32(-3), group.ValidationExponent, name)
		}
	}
}

// Test flow:
// 1. Take a scenario without a validation_threshold.
// 2. Write the runner config.
// 3. Check the runner leaves epoch groups alone, so gencompose keeps its default.
func TestWriteRunnerConfigWithoutThresholdLeavesGroupsToTheDefault(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "catch-up.yaml"))
	require.NoError(t, err)
	require.Nil(t, scenario.Topology.Chain.ValidationThreshold)
	profiles := make(map[string]Profile)
	for _, node := range scenario.Topology.MockML.Nodes {
		profile, err := LoadProfile("profiles", node.Profile)
		require.NoError(t, err)
		profiles[node.Profile] = profile
	}
	workDir := t.TempDir()
	require.NoError(t, writeRunnerConfig(testenvWithBaseConfig(t), workDir, scenario, profiles, false, nil))
	cfg, err := config.Load(filepath.Join(workDir, "config.yaml"))
	require.NoError(t, err)
	require.Empty(t, cfg.EpochGroups)
}

// Test flow:
//  1. Take scenarios whose validation_threshold is 9.58, 1.0 and 1.00, and one at mainnet's 0.958.
//  2. Validate them.
//  3. Check a bar no similarity can pass (1 or more) is refused and 0.958 is accepted.
func TestValidateRefusesAValidationThresholdNoSimilarityCanPass(t *testing.T) {
	for _, bar := range []ChainDecimal{{Value: 958, Exponent: -2}, {Value: 10, Exponent: -1}, {Value: 100, Exponent: -2}} {
		s := testScenario()
		s.Topology.Chain.ValidationThreshold = &bar
		require.ErrorContains(t, s.Validate(), "validation_threshold must be above 0 and below 1", "%d e%d", bar.Value, bar.Exponent)
	}
	s := testScenario()
	s.Topology.Chain.ValidationThreshold = &ChainDecimal{Value: 958, Exponent: -3}
	require.NoError(t, s.Validate())
}
