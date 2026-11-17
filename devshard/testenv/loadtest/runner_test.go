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
	"devshard/testenv/mockchain/ledgerface"
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
		Requests:    4,
		Completed:   2,
		Failed:      2,
		Dropped:     1,
		ErrorRate:   1.0 / 3,
		DroppedRate: 0.25,
	}

	checks := assertRun(scenario, summary, RunInputs{}, nil)
	err := &assertionFailures{Checks: failedChecks(checks)}

	require.Len(t, checks, 2)
	require.Equal(t, "requests.error_rate", checks[0].Name)
	require.False(t, checks[0].Passed)
	require.Equal(t, "<= 0.0000 (0.00%)", checks[0].Expected)
	require.Equal(t, "0.3333 (33.33%)", checks[0].Actual)
	require.Contains(t, checks[0].Details, "http_failures=1")
	require.Equal(t, "requests.dropped_rate", checks[1].Name)
	require.False(t, checks[1].Passed)
	require.Equal(t, "0.2500 (25.00%)", checks[1].Actual)
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

	checks := assertRun(scenario, summary, RunInputs{}, nil)
	err := &assertionFailures{Checks: failedChecks(checks)}

	require.Len(t, checks, 4)
	require.Equal(t, []string{
		"requests.error_rate",
		"requests.dropped_rate",
		"mock_ml.node_used.mock-openai-0",
		"mock_ml.node_used.mock-openai-1",
	}, []string{checks[0].Name, checks[1].Name, checks[2].Name, checks[3].Name})
	require.Contains(t, err.Error(), "3 assertions failed")
}

// Test flow:
// 1. Allow 12% gateway errors and leave the drop bound unset, then set it to 30%.
// 2. Require an unset drop bound to fall back to the error bound, and a set one to win.
func TestAssertRunJudgesGeneratorDropsOnTheirOwnBound(t *testing.T) {
	summary := Summary{Requests: 100, Completed: 80, Failed: 20, Dropped: 20, DroppedRate: 0.2}
	cases := []struct {
		name        string
		droppedRate *float64
		passed      bool
		expected    string
	}{
		{name: "unset bound falls back to the error bound", passed: false, expected: "<= 0.1200 (12.00%)"},
		{name: "own bound", droppedRate: func() *float64 { bound := 0.3; return &bound }(), passed: true, expected: "<= 0.3000 (30.00%)"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			scenario := testScenario()
			scenario.Assertions.Requests.ErrorRate = 0.12
			scenario.Assertions.Requests.DroppedRate = testCase.droppedRate

			checks := assertRun(scenario, summary, RunInputs{}, nil)

			require.Equal(t, "requests.dropped_rate", checks[1].Name)
			require.Equal(t, testCase.passed, checks[1].Passed)
			require.Equal(t, testCase.expected, checks[1].Expected)
		})
	}
}

func TestWaitForNoOrphanedWork_AllowsLoggedGhostPendingWithinThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"finished"},"2":{"status":"pending"}}}`)
	}))
	defer server.Close()

	_, summary, err := waitForNoOrphanedWork(context.Background(), server.URL, "", orphanJudge{MaxGhostRate: 0.5, Timeout: time.Second, Ghosts: newGhostSet([]gatewayBurn{{Nonce: "2"}}, nil)})
	require.NoError(t, err)
	require.Equal(t, 1, summary.Finished)
	require.Zero(t, summary.Orphaned)
}

func TestWaitForNoOrphanedWork_RejectsGhostRateAboveThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"finished"},"2":{"status":"pending"}}}`)
	}))
	defer server.Close()

	_, _, err := waitForNoOrphanedWork(context.Background(), server.URL, "", orphanJudge{MaxGhostRate: 0.25, Timeout: time.Second, Ghosts: newGhostSet([]gatewayBurn{{Nonce: "2"}}, nil)})
	require.ErrorContains(t, err, "ghost inference rate 0.5000")
}

func TestWaitForNoOrphanedWorkDoesNotRequireOneInferencePerRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"finished"}}}`)
	}))
	defer server.Close()

	_, summary, err := waitForNoOrphanedWork(context.Background(), server.URL, "", orphanJudge{Timeout: time.Second})
	require.NoError(t, err)
	require.Equal(t, 1, summary.Total)
	require.Equal(t, 1, summary.Finished)
}

func TestWaitForNoOrphanedWorkRejectsPendingInference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"pending"}}}`)
	}))
	defer server.Close()

	_, _, err := waitForNoOrphanedWork(context.Background(), server.URL, "", orphanJudge{Timeout: 10 * time.Millisecond})
	require.ErrorContains(t, err, "orphaned=1")
	require.ErrorContains(t, err, "pending=1")
}

func TestWaitForNoOrphanedWorkAcceptsTerminalStatuses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"validated"},"2":{"status":"invalidated"},"3":{"status":"timed_out"}}}`)
	}))
	defer server.Close()

	_, summary, err := waitForNoOrphanedWork(context.Background(), server.URL, "", orphanJudge{Timeout: time.Second})
	require.NoError(t, err)
	require.Zero(t, summary.Orphaned)
	require.Equal(t, 3, summary.Total)
}

// Test flow:
// 1. Summarize a deactivated escrow holding a challenged record 15 min old, a challenged record 1 min old and a pending record 15 min old, against a 280 s pending-age bound.
// 2. Require the old challenged record to be orphaned, the young one in flight and the pending one sealed.
func TestSummarizeDebugInferencesJudgesChallengedRecordsByAge(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 20, 0, 0, time.UTC)
	inferences := map[string]debugInference{
		"1": {Status: "challenged", StartedAt: now.Add(-15 * time.Minute).Unix()},
		"2": {Status: "challenged", StartedAt: now.Add(-time.Minute).Unix()},
		"3": {Status: "pending", StartedAt: now.Add(-15 * time.Minute).Unix()},
	}

	summary := summarizeDebugInferences(inferences, nil, escrowDeactivated, now, 280*time.Second)

	require.Equal(t, 1, summary.Orphaned)
	require.Equal(t, 1, summary.InFlight)
	require.Equal(t, 1, summary.Sealed)
}

// Test flow:
// 1. Judge one read under a 280 s pending-age bound holding challenged records 15 min old on an active escrow, on a deactivated escrow the chain has not settled, on one the chain has settled, on a parked escrow of a model with settlement off, and on one the chain read does not show; the settled escrow also holds an old pending record, the unsettled one a young challenged record.
// 2. Require the active, unsettled and unknown escrows' old challenged records to be orphaned and the young one in flight.
// 3. Require the settled and parked escrows' records to be sealed, their challenged ones counted at seal per escrow and named in the check's actual value.
func TestJudgeOrphansSealsChallengedRecordsOnlyOnceTheEscrowIsFinal(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 20, 0, 0, time.UTC)
	old, young := now.Add(-15*time.Minute).Unix(), now.Add(-time.Minute).Unix()
	poll := orphanPoll{Last: &orphanSnapshot{At: now, Runtimes: []orphanRuntime{
		{EscrowID: "1", Active: true, Inferences: map[string]debugInference{"1": {Status: "challenged", StartedAt: old}}},
		{EscrowID: "2", Inferences: map[string]debugInference{"1": {Status: "challenged", StartedAt: old}, "2": {Status: "challenged", StartedAt: young}}},
		{EscrowID: "3", Inferences: map[string]debugInference{"1": {Status: "challenged", StartedAt: old}, "2": {Status: "challenged", StartedAt: old}, "3": {Status: "pending", StartedAt: old}}},
		{EscrowID: "4", Inferences: map[string]debugInference{"1": {Status: "challenged", StartedAt: old}}},
		{EscrowID: "5", Inferences: map[string]debugInference{"1": {Status: "challenged", StartedAt: old}}},
	}}}
	judge := orphanJudge{
		MaxPendingAge: 280 * time.Second,
		Escrows: map[string]ledgerface.EscrowView{
			"1": {ID: 1, ModelID: "model-a"},
			"2": {ID: 2, ModelID: "model-a"},
			"3": {ID: 3, ModelID: "model-a", Settled: true},
			"4": {ID: 4, ModelID: "model-b"},
		},
		SettlementOff: func(model string) bool { return model == "model-b" },
	}

	summary, done, err := judgeOrphans(poll, judge)

	require.False(t, done)
	require.NoError(t, err)
	require.Equal(t, 3, summary.Orphaned)
	require.Equal(t, 1, summary.InFlight)
	require.Equal(t, 4, summary.Sealed)
	require.Equal(t, 3, summary.ChallengedAtSeal)
	require.Equal(t, map[string]int{"3": 2, "4": 1}, summary.ChallengedAtSealByEscrow)
	require.Contains(t, formatTerminalSummary(summary), "challenged_at_seal=3 (escrow 3:2 4:1)")
}

// Test flow:
// 1. Serve a deactivated escrow holding a challenged record 15 min old, and a mock chain that lists the escrow as settled.
// 2. Poll with a 280 s pending-age bound and the chain's URL.
// 3. Require the poll to pass with the record sealed and counted at seal.
func TestWaitForNoOrphanedWorkReadsTheChainForSettledEscrows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/admin/devshards":
			_, _ = fmt.Fprint(w, `{"devshards":[{"id":"7","active":false}]}`)
		case "/devshard/7/v1/debug/inferences":
			_, _ = fmt.Fprintf(w, `{"inferences":{"1":{"status":"challenged","started_at":%d}}}`, time.Now().Add(-15*time.Minute).Unix())
		case "/testenv/escrows":
			_, _ = fmt.Fprint(w, `{"escrows":[{"id":7,"model_id":"model-a","settled":true}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, summary, err := waitForNoOrphanedWork(context.Background(), server.URL, "", orphanJudge{Timeout: 10 * time.Millisecond, MaxPendingAge: 280 * time.Second, ChainURL: server.URL}, true)

	require.NoError(t, err)
	require.Zero(t, summary.Orphaned)
	require.Equal(t, 1, summary.Sealed)
	require.Equal(t, 1, summary.ChallengedAtSeal)
}

// Test flow:
// 1. Write run-summary.json for a run whose last drain read holds challenged records on a settled escrow, on a parked escrow of a model with settlement off and on an active escrow, with the scenario that turns settlement off for that model.
// 2. Require its challenged_at_seal block to count the settled and parked escrows' records per escrow and leave the active one out.
// 3. Require a run without its scenario to carry no challenged_at_seal block.
func TestWriteRunSummaryCountsChallengedRecordsAtSeal(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 20, 0, 0, time.UTC)
	challenged := map[string]debugInference{"1": {Status: "challenged", StartedAt: now.Add(-15 * time.Minute).Unix()}}
	off := false
	scenario := Scenario{Gateway: Gateway{EscrowRotation: GatewayEscrowRotation{Enabled: true, SettlementEnabled: true, Models: []GatewayRotationModel{{Model: "model-b", SettlementEnabled: &off}}}}}
	scenario.Assertions.Invariants.Enabled = true
	run := runReport{
		summary: Summary{Scenario: "s", StartedAt: now.Add(-20 * time.Minute), Duration: time.Minute},
		inputs: RunInputs{
			Orphans: &orphanPoll{Last: &orphanSnapshot{At: now, Runtimes: []orphanRuntime{
				{EscrowID: "1", Inferences: challenged},
				{EscrowID: "2", Inferences: challenged},
				{EscrowID: "3", Active: true, Inferences: challenged},
			}}},
			ChainLedger: &chainLedger{Escrows: []ledgerface.EscrowView{{ID: 1, ModelID: "model-a", Settled: true}, {ID: 2, ModelID: "model-b"}, {ID: 3, ModelID: "model-a"}}},
		},
		scenario: &scenario,
	}

	summary, err := writeRunSummary(dir, run)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, runSummaryFileName))
	require.NoError(t, err)
	var decoded struct {
		ChallengedAtSeal *RunSummaryChallengedAtSeal `json:"challenged_at_seal"`
	}
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Equal(t, &RunSummaryChallengedAtSeal{Records: 2, ByEscrow: map[string]int{"1": 1, "2": 1}}, decoded.ChallengedAtSeal)
	require.Equal(t, decoded.ChallengedAtSeal, summary.ChallengedAtSeal)

	run.scenario = nil
	summary, err = writeRunSummary(t.TempDir(), run)
	require.NoError(t, err)
	require.Nil(t, summary.ChallengedAtSeal)
}

// Test flow:
// 1. Serve a deactivated escrow with a pending record and an active escrow whose work finished.
// 2. Require the deactivated escrow's record to count as sealed, not orphaned.
func TestWaitForNoOrphanedWorkLeavesDeactivatedEscrowsToTheSettleCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/admin/devshards":
			_, _ = fmt.Fprint(w, `{"devshards":[{"id":"1","active":false},{"id":"2","active":true}]}`)
		case "/devshard/1/v1/debug/inferences":
			_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"pending"}}}`)
		case "/devshard/2/v1/debug/inferences":
			_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"finished"}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, summary, err := waitForNoOrphanedWork(context.Background(), server.URL, "", orphanJudge{Timeout: 10 * time.Millisecond}, true)

	require.NoError(t, err)
	require.Zero(t, summary.Orphaned)
	require.Equal(t, 1, summary.Sealed)
}

// Test flow:
// 1. Serve an active escrow with a pending record younger than the pending-age bound, then one older.
// 2. Require the young record to count as in flight and the old one as orphaned.
func TestWaitForNoOrphanedWorkJudgesActiveRecordsByTheirAge(t *testing.T) {
	cases := []struct {
		name     string
		age      time.Duration
		orphaned int
		inFlight int
	}{
		{name: "younger than the bound is in flight", age: 10 * time.Second, inFlight: 1},
		{name: "older than the bound is orphaned", age: 2 * time.Minute, orphaned: 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			startedAt := time.Now().Add(-testCase.age).Unix()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"inferences":{"1":{"status":"pending","started_at":%d}}}`, startedAt)
			}))
			defer server.Close()

			_, summary, _ := waitForNoOrphanedWork(context.Background(), server.URL, "", orphanJudge{Timeout: 10 * time.Millisecond, MaxPendingAge: time.Minute})

			require.Equal(t, testCase.orphaned, summary.Orphaned)
			require.Equal(t, testCase.inFlight, summary.InFlight)
		})
	}
}

func TestReadGatewayBurnsJSONLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.log")
	log := `devshardctl-1 | {"time":"2026-10-14T12:28:14Z","level":"INFO","msg":"ghost_probe_skipped","stage":"ghost_probe_skipped","escrow":"1","nonce":271,"reason":"participant_catching_up_no_send"}` + "\n" +
		`devshardctl-1 | {"time":"2026-10-14T12:28:14Z","level":"INFO","msg":"started","stage":"started","escrow":"1","nonce":272}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(log), 0o644))

	burns, err := readGatewayBurns(path)
	require.NoError(t, err)
	require.Equal(t, []gatewayBurn{
		{At: time.Date(2026, 10, 14, 12, 28, 14, 0, time.UTC), EscrowID: "1", Nonce: "271", Reason: "participant_catching_up_no_send"},
	}, burns)
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

// Test flow:
// 1. Serve two escrows on /v1/admin/devshards and each one's debug inferences as the gateway's nonce-carrying list.
// 2. Take an aggregated orphan snapshot.
// 3. Check every record of both escrows is read, keyed by nonce, with its status.
func TestFetchOrphanSnapshot_ReadsGatewayPerEscrowLists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/admin/devshards":
			_, _ = w.Write([]byte(`{"devshards":[{"id":"7","active":true},{"id":"8","active":true}]}`))
		case "/devshard/7/v1/debug/inferences":
			_, _ = w.Write([]byte(`{"escrow_id":"7","inferences":[{"nonce":1,"status":"finished","record":{}},{"nonce":2,"status":"started","record":{}}]}`))
		case "/devshard/8/v1/debug/inferences":
			_, _ = w.Write([]byte(`{"escrow_id":"8","inferences":[{"nonce":1,"status":"validated","record":{}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snapshot, err := fetchOrphanSnapshot(context.Background(), server.Client(), server.URL, "", true)

	require.NoError(t, err)
	require.Len(t, snapshot.Runtimes, 2)
	require.Equal(t, "7", snapshot.Runtimes[0].EscrowID)
	require.Equal(t, map[string]debugInference{"1": {Status: "finished"}, "2": {Status: "started"}}, snapshot.Runtimes[0].Inferences)
	require.Equal(t, map[string]debugInference{"1": {Status: "validated"}}, snapshot.Runtimes[1].Inferences)
}

func TestDecodeDebugInferences_ReadsDevshardctlNonceMap(t *testing.T) {
	inferences, err := decodeDebugInferences(json.RawMessage(`{"5":{"status":"timed_out"}}`))

	require.NoError(t, err)
	require.Equal(t, map[string]debugInference{"5": {Status: "timed_out"}}, inferences)
}

func TestApplyCmdGatewayScenarioSettings_SendsOverridesAndToleratesRefusal(t *testing.T) {
	var method string
	var overrides map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		require.NoError(t, json.NewDecoder(r.Body).Decode(&overrides))
		http.Error(w, "refused", http.StatusBadRequest)
	}))
	defer server.Close()
	scenario := testScenario()
	scenario.Gateway.Redundancy.SecondaryWaitAfterWinner = "30s"
	scenario.Gateway.ParticipantThrottle.HTTPQuarantine = "70s"

	err := applyCmdGatewayScenarioSettings(context.Background(), server.URL, "admin", scenario, nil)

	require.NoError(t, err)
	require.Equal(t, http.MethodPut, method)
	require.Equal(t, map[string]any{"engine_loser_grace_ms": float64(30000), "rotation_enabled": false}, overrides)
}

// Test flow:
// 1. Build checks for a known_bug case (one failing, one passing), a plain case and no case.
// 2. Mark them without and with strict mode.
// 3. Check failures become passing XFAIL, passes become XPASS (failing only when strict), and other checks are untouched.
func TestMarkKnownBugs(t *testing.T) {
	cases := []Case{{ID: "host-over-peer-chat-budget", KnownBug: "#1988"}, {ID: "receipt-then-empty-stream"}}
	checks := []AssertionResult{
		{Name: "i6.no_quarantine", Case: "host-over-peer-chat-budget", Passed: false, Details: "P4 quarantined"},
		{Name: "i1.no_misses", Case: "host-over-peer-chat-budget", Passed: true},
		{Name: "i6.reason", Case: "receipt-then-empty-stream", Passed: false},
		{Name: "requests.error_rate", Passed: true},
		{Name: "i8.recovery", Case: "host-over-peer-chat-budget", Passed: true, Status: statusNotJudged},
	}
	marked := markKnownBugs(checks, cases, false)
	require.Equal(t, checks[4], marked[4])
	require.Equal(t, "XFAIL", marked[0].Status)
	require.True(t, marked[0].Passed)
	require.Equal(t, "known_bug #1988; P4 quarantined", marked[0].Details)
	require.Equal(t, "XPASS", marked[1].Status)
	require.True(t, marked[1].Passed)
	require.Equal(t, checks[2], marked[2])
	require.Equal(t, checks[3], marked[3])
	require.Equal(t, []AssertionResult{marked[2]}, failedChecks(marked))
	strict := markKnownBugs(checks, cases, true)
	require.False(t, strict[1].Passed)
	require.Len(t, failedChecks(strict), 2)
	require.False(t, checks[0].Passed)
}

// Test flow:
// 1. Decode a recorded docker compose ls payload.
// 2. Check a running loadtest-* project blocks the start, other projects do not, and a listing error is reported.
func TestRefuseConcurrentStacks(t *testing.T) {
	names, err := parseComposeProjects([]byte(`[{"Name":"loadtest-dm37aoxwypts","Status":"running(12)","ConfigFiles":"/tmp/a.yml"},{"Name":"testenv","Status":"running(3)"}]`))
	require.NoError(t, err)
	require.Equal(t, []string{"loadtest-dm37aoxwypts", "testenv"}, names)
	list := func(projects []string, err error) func(context.Context) ([]string, error) {
		return func(context.Context) ([]string, error) { return projects, err }
	}
	require.ErrorContains(t, refuseConcurrentStacks(context.Background(), list(names, nil)), "loadtest-dm37aoxwypts")
	require.NoError(t, refuseConcurrentStacks(context.Background(), list([]string{"testenv", "citest-1"}, nil)))
	require.NoError(t, refuseConcurrentStacks(context.Background(), list(nil, nil)))
	require.ErrorContains(t, refuseConcurrentStacks(context.Background(), list(nil, errors.New("docker down"))), "docker down")
	_, err = parseComposeProjects([]byte("not json"))
	require.Error(t, err)
}

// Test flow:
// 1. Apply gateway settings with the two new quarantines and per-model settlement overrides to a fake admin endpoint.
// 2. Check the throttle keys in milliseconds, and that settlement_enabled is set only on overridden models.
// 3. Check an override for a model that is not rotated is rejected.
func TestApplyGatewayChunkASettings(t *testing.T) {
	scenario := mixScenario()
	scenario.Gateway.ParticipantThrottle = GatewayParticipantThrottle{EmptyStreamQuarantine: "45s", StalledWinnerQuarantine: "1m"}
	disabled := false
	scenario.Gateway.EscrowRotation = GatewayEscrowRotation{Enabled: true, SettlementEnabled: true, PrePoCBlocks: 10, TempCount: 1, TargetCount: 2, Amount: 5, PrivateKeyEnv: "KEY",
		Models: []GatewayRotationModel{{Model: "model-b", SettlementEnabled: &disabled}, {Model: "model-a"}}}
	var body struct {
		Throttle map[string]int64 `json:"participant_throttle"`
		Rotation struct {
			SettlementEnabled bool             `json:"settlement_enabled"`
			Models            []map[string]any `json:"models"`
		} `json:"escrow_rotation"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	models := rotationModelIDs(nil, nil, scenario)
	require.Equal(t, []string{"model-a", "model-b"}, models)
	require.NoError(t, applyGatewayScenarioSettings(context.Background(), server.URL, "", scenario, models))
	require.Equal(t, map[string]int64{"empty_stream_quarantine_ms": 45000, "stalled_winner_quarantine_ms": 60000}, body.Throttle)
	require.True(t, body.Rotation.SettlementEnabled)
	require.Len(t, body.Rotation.Models, 2)
	require.NotContains(t, body.Rotation.Models[0], "settlement_enabled")
	require.Equal(t, false, body.Rotation.Models[1]["settlement_enabled"])
	require.ErrorContains(t, applyGatewayScenarioSettings(context.Background(), server.URL, "", scenario, []string{"model-a"}), "model-b is not a rotated model")
}

// Test flow:
// 1. Write the runner config from a minimal base config for a 3-participant scenario with per-host env, ML node bindings and a two-model mix.
// 2. Load the written config.
// 3. Check host env and node bindings are threaded through, and each mix model has its own escrow and epoch group.
func TestWriteRunnerConfigThreadsChunkAFields(t *testing.T) {
	scenario := mixScenario()
	scenario.Topology.Participants = 3
	scenario.Topology.Hosts = []TopologyHost{{ID: "versiond-3", Env: map[string]string{"DEVSHARD_RPC_MSGS_PER_MIN": "3000"}}}
	scenario.Topology.MockML.Nodes[1].Host = "versiond-3"
	require.NoError(t, scenario.Validate())
	profiles := map[string]Profile{"fast": {TTFT: "1ms", TokenInterval: "1ms", Workers: 1}}
	testenvDir := t.TempDir()
	base := &config.File{Hosts: []config.HostCfg{{ID: "versiond-0"}, {ID: "versiond-1"}, {ID: "versiond-2"}}, Escrows: []config.Escrow{{ID: 1, ModelID: "test-model"}}}
	base.ApplyDefaults()
	require.NoError(t, os.MkdirAll(filepath.Join(testenvDir, "config"), 0o755))
	require.NoError(t, base.Save(filepath.Join(testenvDir, "config", "config.yaml")))
	workDir := t.TempDir()
	require.NoError(t, writeRunnerConfig(testenvDir, workDir, scenario, profiles, false, nil))
	cfg, err := config.Load(filepath.Join(workDir, "config.yaml"))
	require.NoError(t, err)
	require.Len(t, cfg.Hosts, 4)
	require.Equal(t, map[string]string{"DEVSHARD_RPC_MSGS_PER_MIN": "3000"}, cfg.Hosts[3].Env)
	require.Empty(t, cfg.Hosts[2].Env)
	require.Equal(t, "", cfg.MockOpenAI.Nodes[0].Host)
	require.Equal(t, "versiond-3", cfg.MockOpenAI.Nodes[1].Host)
	var escrowModels, groupModels []string
	for _, escrow := range cfg.Escrows {
		escrowModels = append(escrowModels, escrow.ModelID)
	}
	for _, group := range cfg.EpochGroups {
		groupModels = append(groupModels, group.ModelID)
	}
	require.Equal(t, []string{"model-a", "model-b"}, escrowModels)
	require.Equal(t, []string{"model-a", "model-b"}, groupModels)
}

// Test flow:
// 1. Turn one complete, one failed and one never-reverted event record into assertions.
// 2. Check only the complete record passes and the error is carried in details.
func TestEventAssertions(t *testing.T) {
	now := time.Now().UTC()
	checks := eventAssertions([]EventRecord{
		{ID: "c1", Action: "ml_fault", Target: "mock-openai-3", StartedAt: &now, EndedAt: &now},
		{ID: "c6", Action: "gateway_restart", Target: "devshardctl", Error: "kill failed"},
		{ID: "c3", Action: "pause", Target: "versiond-3", StartedAt: &now},
	})
	require.Equal(t, "events.c1.ml_fault.mock-openai-3.applied", checks[0].Name)
	require.True(t, checks[0].Passed)
	require.False(t, checks[1].Passed)
	require.Equal(t, "kill failed", checks[1].Details)
	require.False(t, checks[2].Passed)
	require.Contains(t, checks[2].Actual, "ended=<none>")
}

// Test flow:
// 1. Write the runner config from a testenv dir with no config/config.yaml, as in a clean worktree.
// 2. Load the written config.
// 3. Check the participant hosts, escrow amount and ML nodes come from the scenario.
func TestWriteRunnerConfigWithoutBaseConfig(t *testing.T) {
	scenario := mixScenario()
	scenario.Topology.Participants = 5
	scenario.Topology.Chain.EscrowAmount = 123456
	scenario.Topology.MockML.Nodes[1].Host = "versiond-3"
	require.NoError(t, scenario.Validate())
	profiles := map[string]Profile{"fast": {TTFT: "1ms", TokenInterval: "1ms", Workers: 1}}
	workDir := t.TempDir()
	require.NoError(t, writeRunnerConfig(t.TempDir(), workDir, scenario, profiles, false, nil))
	cfg, err := config.Load(filepath.Join(workDir, "config.yaml"))
	require.NoError(t, err)
	require.Len(t, cfg.Hosts, 6)
	require.NotEmpty(t, cfg.Escrows)
	for _, escrow := range cfg.Escrows {
		require.Equal(t, uint64(123456), escrow.Amount)
	}
	require.Len(t, cfg.MockOpenAI.Nodes, len(scenario.Topology.MockML.Nodes))
}

// Test flow:
// 1. Load every long-escrow scenario and judge a 30 min run at 32 rps that dropped 91 of 57 600 requests in one stall, the worst saved run.
// 2. Require requests.dropped_rate to pass against a 0.5% bound, and to fail at 300 drops.
func TestLongEscrowScenariosAllowHalfAPercentOfDrops(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("scenarios", "long-escrow*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		scenario, err := LoadScenario(path)
		require.NoError(t, err, path)
		for dropped, passed := range map[int]bool{91: true, 300: false} {
			summary := Summary{Requests: 57_600, Completed: 57_600 - dropped, Dropped: dropped, DroppedRate: float64(dropped) / 57_600}
			checks := assertRun(scenario, summary, RunInputs{}, nil)
			require.Equal(t, "requests.dropped_rate", checks[1].Name)
			require.Equal(t, passed, checks[1].Passed, "%s dropped=%d", path, dropped)
			require.Equal(t, "<= 0.0050 (0.50%)", checks[1].Expected, path)
		}
	}
}
