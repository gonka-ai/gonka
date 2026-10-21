package loadtest

import (
	"context"
	"encoding/json"
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

// Test flow:
// 1. Parse probe output with the shard list, rpc stats, one shard detail and one empty route.
// 2. Check nonce, root, host stats, group, rpc bans and the empty-route error.
func TestParseHostStatsProbe(t *testing.T) {
	output := strings.Join([]string{
		"shards\t" + `{"current_epoch_id":3,"active_escrows":["11","12"],"shards":[]}`,
		"rpc\t" + `{"minute_unix":1,"host":{"requests":90,"banned":7,"zones":[{"zone":"chat","requests":50,"banned":7},{"zone":"diff","requests":40,"banned":0}]}}`,
		"11\t" + `{"escrow_id":"11","nonce":42,"state_root_and_protocol_version":"ab:v6","host_stats":{"0":{"missed":1,"invalid":0,"cost":10},"1":{"missed":0,"invalid":2,"cost":20}},"group":[{"SlotID":0,"ValidatorAddress":"p0"},{"SlotID":1,"ValidatorAddress":"p1"}]}`,
		"12\t",
	}, "\n") + "\n"
	now := time.Unix(100, 0).UTC()
	samples := parseHostStatsProbe(output, now, "versiond-2", "p1")
	require.Len(t, samples, 4)
	require.Equal(t, []string{"11", "12"}, samples[0].ActiveEscrows)
	require.Equal(t, uint64(90), samples[1].RPCRequests)
	require.Equal(t, uint64(7), samples[1].RPCBanned)
	require.Equal(t, map[string]uint64{"chat": 7}, samples[1].RPCZoneBanned)
	detail := samples[2]
	require.Equal(t, "shard", detail.Kind)
	require.Equal(t, uint64(42), detail.Nonce)
	require.Equal(t, "ab:v6", detail.StateRoot)
	require.Equal(t, HostSlotStats{Missed: 1, Cost: 10}, detail.HostStats["0"])
	require.Equal(t, []HostSlot{{0, "p0"}, {1, "p1"}}, detail.Group)
	require.Equal(t, "p1", detail.Participant)
	require.Equal(t, "12", samples[3].EscrowID)
	require.Equal(t, "empty response", samples[3].Error)
}

// Test flow:
// 1. Parse psql tab-separated output, then a malformed row.
// 2. Check the rows and the error.
func TestParseLeaseCounts(t *testing.T) {
	rows, err := parseLeaseCounts("p0\tsubmitted\t12\np2\tskipped\t3\n")
	require.NoError(t, err)
	require.Equal(t, []LeaseCount{{"p0", "submitted", 12}, {"p2", "skipped", 3}}, rows)
	rows, err = parseLeaseCounts("\n")
	require.NoError(t, err)
	require.Empty(t, rows)
	_, err = parseLeaseCounts("p0\tskipped\n")
	require.Error(t, err)
}

// Test flow:
// 1. Fake docker compose so only the listed Postgres services run, each returning one lease row named after itself.
// 2. Query a per_participant stack: require one row from each participant's Postgres and no error.
// 3. Stop one participant's Postgres: require the other's rows and an error naming the stopped service.
// 4. Exclude only versiond-1, an HA replica sharing versiond-0's Postgres: require that Postgres to be probed still.
// 5. Query a shared stack: require the single devshard-postgres to be read.
func TestQueryLeaseCountsPerParticipant(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\ncase \" $FAKE_PG_RUNNING \" in\n*\" $8 \"*) printf '%s\\tskipped\\t1\\n' \"$8\" ;;\n*) echo \"service \\\"$8\\\" is not running\" >&2; exit 1 ;;\nesac\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &config.File{
		Versiond: config.VersiondCfg{Mode: config.VersiondModeMulti},
		Hosts:    []config.HostCfg{{ID: "versiond-0"}, {ID: "versiond-1"}, {ID: "versiond-2"}},
		Postgres: config.PostgresCfg{Enabled: true, PerParticipant: true},
	}
	cfg.ApplyDefaults()
	ctx := context.Background()

	t.Setenv("FAKE_PG_RUNNING", "devshard-postgres-versiond-0 devshard-postgres-versiond-2")
	rows, err := queryLeaseCounts(ctx, t.TempDir(), "project", "compose.yaml", cfg, nil)
	require.NoError(t, err)
	require.Equal(t, []LeaseCount{{"devshard-postgres-versiond-0", "skipped", 1}, {"devshard-postgres-versiond-2", "skipped", 1}}, rows)

	t.Setenv("FAKE_PG_RUNNING", "devshard-postgres-versiond-0")
	rows, err = queryLeaseCounts(ctx, t.TempDir(), "project", "compose.yaml", cfg, nil)
	require.ErrorContains(t, err, `service "devshard-postgres-versiond-2" is not running`)
	require.Equal(t, []LeaseCount{{"devshard-postgres-versiond-0", "skipped", 1}}, rows)

	t.Setenv("FAKE_PG_RUNNING", "devshard-postgres-versiond-2")
	rows, err = queryLeaseCounts(ctx, t.TempDir(), "project", "compose.yaml", cfg, map[string]string{"versiond-1": sqliteLeaseExclusion})
	require.ErrorContains(t, err, `service "devshard-postgres-versiond-0" is not running`)
	require.Equal(t, []LeaseCount{{"devshard-postgres-versiond-2", "skipped", 1}}, rows)

	cfg.Postgres.PerParticipant = false
	t.Setenv("FAKE_PG_RUNNING", "devshard-postgres")
	rows, err = queryLeaseCounts(ctx, t.TempDir(), "project", "compose.yaml", cfg, nil)
	require.NoError(t, err)
	require.Equal(t, []LeaseCount{{"devshard-postgres", "skipped", 1}}, rows)
}

// Test flow:
// 1. Fake docker compose so versiond-4's Postgres has no lease table and every other running Postgres returns a submitted lease.
// 2. Exclude versiond-4 as a sqlite host from the scenario, query the leases and evaluate no_skipped_leases: require a pass.
// 3. Stop versiond-2's Postgres too: require no_skipped_leases to fail as unavailable, naming versiond-2 and not versiond-4.
func TestQueryLeaseCountsSkipsExcludedHosts(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\ncase \" $FAKE_PG_RUNNING \" in\n*\" $8 \"*) printf '%s\\tsubmitted\\t1\\n' \"$8\" ;;\n*) echo \"ERROR:  relation \\\"devshard_validation_leases\\\" does not exist ($8)\" >&2; exit 1 ;;\nesac\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &config.File{
		Versiond: config.VersiondCfg{Mode: config.VersiondModeMulti},
		Hosts:    []config.HostCfg{{ID: "versiond-0"}, {ID: "versiond-1"}, {ID: "versiond-2"}, {ID: "versiond-3"}, {ID: "versiond-4"}},
		Postgres: config.PostgresCfg{Enabled: true, PerParticipant: true},
	}
	cfg.ApplyDefaults()
	var scenario Scenario
	for _, h := range cfg.Hosts {
		scenario.Topology.Hosts = append(scenario.Topology.Hosts, TopologyHost{ID: h.ID})
	}
	scenario.Topology.Hosts[4].Env = map[string]string{"DEVSHARD_STORAGE_MODE": "sqlite"}
	excluded := leaseExcludedHosts(scenario)
	require.Equal(t, map[string]string{"versiond-4": sqliteLeaseExclusion}, excluded)
	evaluate := func() AssertionResult {
		in := passingInputs()
		in.LeaseExcluded = excluded
		rows, err := queryLeaseCounts(context.Background(), t.TempDir(), "project", "compose.yaml", cfg, excluded)
		in.Leases, in.LeasesError = rows, ""
		if err != nil {
			in.LeasesError = err.Error()
		}
		checks, _ := evaluateInvariants(in)
		return checksNamed(t, checks, "no-skipped-validation-leases.no_skipped_leases")[0]
	}

	t.Setenv("FAKE_PG_RUNNING", "devshard-postgres-versiond-0 devshard-postgres-versiond-1 devshard-postgres-versiond-2 devshard-postgres-versiond-3")
	check := evaluate()
	require.True(t, check.Passed, check.Details)
	require.Contains(t, check.Expected, "versiond-4 excluded")

	t.Setenv("FAKE_PG_RUNNING", "devshard-postgres-versiond-0 devshard-postgres-versiond-1 devshard-postgres-versiond-3")
	check = evaluate()
	require.False(t, check.Passed)
	require.Equal(t, "unavailable", check.Actual)
	require.Contains(t, check.Details, "devshard-postgres-versiond-2")
	require.NotContains(t, check.Details, "devshard-postgres-versiond-4")
}

// Test flow:
// 1. Parse a /metrics body with gateway counters, a histogram, the fork counter and runtime series.
// 2. Check only domain series are kept and their keys round-trip through parseDomainKey.
func TestParseDomainMetrics(t *testing.T) {
	body := `# TYPE devshard_gateway_participant_quarantine_transitions_total counter
devshard_gateway_participant_quarantine_transitions_total{participant_key="p2",model="m-a",mode="shadow",reason="empty_stream_quarantine"} 3
# TYPE devshard_gateway_attempt_latency_seconds histogram
devshard_gateway_attempt_latency_seconds_bucket{le="1"} 2
devshard_gateway_attempt_latency_seconds_bucket{le="+Inf"} 4
devshard_gateway_attempt_latency_seconds_sum 5.5
devshard_gateway_attempt_latency_seconds_count 4
# TYPE devshard_diff_fork_detected_total counter
devshard_diff_fork_detected_total 0
# TYPE go_goroutines gauge
go_goroutines 12
`
	values := parseDomainMetrics(body)
	key := `devshard_gateway_participant_quarantine_transitions_total{mode="shadow",model="m-a",participant_key="p2",reason="empty_stream_quarantine"}`
	require.Equal(t, map[string]float64{
		key: 3,
		`devshard_gateway_attempt_latency_seconds_count{}`: 4,
		`devshard_gateway_attempt_latency_seconds_sum{}`:   5.5,
		`devshard_diff_fork_detected_total{}`:              0,
	}, values)
	name, labels := parseDomainKey(key)
	require.Equal(t, quarantineFamily, name)
	require.Equal(t, map[string]string{"mode": "shadow", "model": "m-a", "participant_key": "p2", "reason": "empty_stream_quarantine"}, labels)
	require.Nil(t, parseDomainMetrics("go_goroutines 3\n"))
	require.Nil(t, parseDomainMetrics("not metrics {"))
}

// Test flow:
// 1. Summarize inferences in pending, started and terminal states.
// 2. Check the open count, the oldest age and the oldest status.
func TestSummarizePending(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	sample := PendingSample{At: now}
	summarizePending(&sample, map[string]DebugInference{
		"1": {Status: "pending", StartedAt: 990},
		"2": {Status: "started", StartedAt: 900},
		"3": {Status: "finished", StartedAt: 100},
		"4": {Status: "started"},
	})
	require.Equal(t, 3, sample.Open)
	require.Equal(t, 100.0, sample.MaxAgeSeconds)
	require.Equal(t, "2", sample.OldestInference)
	require.Equal(t, "started", sample.OldestStatus)
}

// Test flow:
// 1. Resolve settlement for models with the global flag on and off, with and without overrides.
// 2. Check rotation disabled yields no disabled model.
func TestSettlementDisabledModels(t *testing.T) {
	off, on := false, true
	s := Scenario{}
	s.Gateway.EscrowRotation = GatewayEscrowRotation{Enabled: true, SettlementEnabled: true, Models: []GatewayRotationModel{{Model: "m-b", SettlementEnabled: &off}, {Model: "m-c"}}}
	require.Equal(t, map[string]bool{"m-b": true}, settlementDisabledModels(s, []string{"m-a", "m-b", "m-c"}))
	s.Gateway.EscrowRotation.SettlementEnabled = false
	s.Gateway.EscrowRotation.Models[1].SettlementEnabled = &on
	require.Equal(t, map[string]bool{"m-a": true, "m-b": true}, settlementDisabledModels(s, []string{"m-a", "m-b", "m-c"}))
	s.Gateway.EscrowRotation.Enabled = false
	require.Empty(t, settlementDisabledModels(s, []string{"m-a"}))
}

// Test flow:
// 1. Resolve settings for 5s/30s protocol timeouts and configured quarantines.
// 2. Check grace (refusal + execution + sweep grace + sweep tick), the pending-age bound with the sweep grace, and window defaults and overrides.
func TestResolveInvariantSettings(t *testing.T) {
	s := Scenario{Protocol: Protocol{RefusalTimeout: "5s", ExecutionTimeout: "30s"}, Workload: Workload{Duration: "15m"}}
	s.Gateway.ParticipantThrottle = GatewayParticipantThrottle{HTTPQuarantine: "45s", EmptyStreamQuarantine: "45s"}
	settings := resolveInvariantSettings(s)
	require.Equal(t, 5*time.Second+30*time.Second+2*time.Minute+5*time.Second+30*time.Second, settings.Grace)
	require.Equal(t, 5*time.Second+30*time.Second+2*time.Minute+5*time.Second+30*time.Second+30*time.Second, settings.MaxPendingAge)
	require.Equal(t, [2]time.Duration{time.Minute, 2 * time.Minute}, settings.Baseline)
	require.Equal(t, [2]time.Duration{2 * time.Minute, 4 * time.Minute}, settings.TrendEarly)
	require.Equal(t, [2]time.Duration{13 * time.Minute, 15 * time.Minute}, settings.TrendLate)
	require.Equal(t, map[string]time.Duration{"http": 45 * time.Second, "empty_stream": 45 * time.Second, "sender_rate": time.Second}, settings.Quarantine)
	s.Assertions.Invariants = InvariantAssertions{Grace: "10s", MaxPendingAge: "1m", TrendEarly: InvariantWindow{From: "3m", To: "5m"}}
	settings = resolveInvariantSettings(s)
	require.Equal(t, 10*time.Second, settings.Grace)
	require.Equal(t, time.Minute, settings.MaxPendingAge)
	require.Equal(t, [2]time.Duration{3 * time.Minute, 5 * time.Minute}, settings.TrendEarly)
}

// Test flow:
// 1. Validate invariant settings: a good block, a per-check case binding, a bad duration, a window past the workload, an unknown invariant and an undeclared case.
// 2. Check each error.
func TestValidateInvariants(t *testing.T) {
	base := Scenario{Cases: []Case{{ID: "escrow-rotation-per-model-settlement"}}}
	base.Assertions.Invariants = InvariantAssertions{Enabled: true, Grace: "95s", Baseline: InvariantWindow{From: "1m", To: "2m"}, Cases: map[string]string{"no-runtime-for-deactivated-escrow": "escrow-rotation-per-model-settlement"}}
	require.NoError(t, base.validateInvariants(15*time.Minute))
	perCheck := base
	perCheck.Assertions.Invariants.Cases = map[string]string{"escrow-accounting-reconciles.final_nonce_le_max": "escrow-rotation-per-model-settlement", "no-escrow-falsely-marked-missing": "escrow-rotation-per-model-settlement"}
	require.NoError(t, perCheck.validateInvariants(15*time.Minute))
	for name, mutate := range map[string]func(*InvariantAssertions){
		"bad grace":       func(inv *InvariantAssertions) { inv.Grace = "soon" },
		"window too late": func(inv *InvariantAssertions) { inv.TrendLate = InvariantWindow{From: "14m", To: "16m"} },
		"unknown name": func(inv *InvariantAssertions) {
			inv.Cases = map[string]string{"no-such-invariant": "escrow-rotation-per-model-settlement"}
		},
		"bad check name": func(inv *InvariantAssertions) {
			inv.Cases = map[string]string{"escrow-accounting-reconciles.Final-Nonce": "escrow-rotation-per-model-settlement"}
		},
		"undeclared case": func(inv *InvariantAssertions) {
			inv.Cases = map[string]string{"no-runtime-for-deactivated-escrow": "host-public-port-down"}
		},
		"inverted window": func(inv *InvariantAssertions) { inv.Baseline = InvariantWindow{From: "2m", To: "1m"} },
	} {
		t.Run(name, func(t *testing.T) {
			s := base
			s.Assertions.Invariants.Cases = map[string]string{"no-runtime-for-deactivated-escrow": "escrow-rotation-per-model-settlement"}
			mutate(&s.Assertions.Invariants)
			require.Error(t, s.validateInvariants(15*time.Minute))
		})
	}
}

// Test flow:
// 1. Plan accounting boundaries for a windowed event and an epoch advance.
// 2. Check start, end + grace and the pre-epoch snapshot times.
func TestAccountingBoundaries(t *testing.T) {
	started := time.Unix(0, 0).UTC()
	events := []plannedEvent{
		{Event: Event{ID: "c1", Action: "ml_fault"}, window: eventWindow{start: 2 * time.Minute, end: 3 * time.Minute}},
		{Event: Event{ID: "c8", Action: "epoch_advance"}, window: eventWindow{start: 7 * time.Minute, end: 8 * time.Minute}},
	}
	got := accountingBoundaries(started, events, 95*time.Second)
	require.Equal(t, []labeledTime{
		{started.Add(2 * time.Minute), "c1:start"},
		{started.Add(3*time.Minute + 95*time.Second), "c1:end+grace"},
		{started.Add(7 * time.Minute), "c8:start"},
		{started.Add(8*time.Minute + 95*time.Second), "c8:end+grace"},
		{started.Add(7*time.Minute - time.Second), "c8:pre_epoch"},
	}, got)
}

// Test flow:
// 1. Start a poller with a first label and a two-entry time schedule.
// 2. Stop it after both fire and check labels, the final sample and the JSONL artifact.
func TestJSONLPollerSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	now := time.Now()
	collect := func(_ context.Context, label string) []AccountingSnapshot {
		return []AccountingSnapshot{{Label: label}}
	}
	poller, err := startJSONLPoller(context.Background(), path, "baseline", atTimes([]labeledTime{{now.Add(40 * time.Millisecond), "b"}, {now.Add(20 * time.Millisecond), "a"}}), collect)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		poller.mu.Lock()
		defer poller.mu.Unlock()
		return len(poller.samples) == 3
	}, 2*time.Second, 5*time.Millisecond)
	samples := poller.stop("final")
	labels := []string{}
	for _, s := range samples {
		labels = append(labels, s.Label)
	}
	require.Equal(t, []string{"baseline", "a", "b", "final"}, labels)
	require.Equal(t, samples, poller.stop("again"))
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Len(t, strings.Split(strings.TrimSpace(string(body)), "\n"), 4)
}

// Test flow:
// 1. Serve a fake gateway with an active resident escrow, a deactivated resident one and a retired one.
// 2. Fetch devshards and collect throttle and pending samples for resident escrows only.
func TestGatewayPollersAgainstFake(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		require.Equal(t, "Bearer key", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/admin/devshards":
			_, _ = w.Write([]byte(`{"devshards":[{"id":"11","model":"m-a","active":true,"runtime":{"nonce":9}},{"id":"12","model":"m-b","active":false,"runtime":{"nonce":4}},{"id":"10","model":"m-a","active":false}]}`))
		case "/v1/admin/devshards/11/participants", "/v1/admin/devshards/12/participants":
			_, _ = w.Write([]byte(`{"participants":[{"participant_key":"p2","quarantined":false,"shadow_quarantined":true,"quarantine_until":"2026-01-01T00:00:45Z","failure_strikes":3,"blocked":false}]}`))
		case "/devshard/11/v1/debug/pending", "/devshard/12/v1/debug/pending":
			_, _ = w.Write([]byte(`{"nonce":9,"pending":[{"type":"finish","id":3}],"warm_keys":[]}`))
		case "/devshard/11/v1/debug/inferences", "/devshard/12/v1/debug/inferences":
			body, _ := json.Marshal(map[string]any{"inferences": map[string]any{"3": map[string]any{"status": "started", "started_at": time.Now().Add(-time.Minute).Unix()}}})
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	devshards, err := fetchGatewayDevshards(context.Background(), server.URL, "key")
	require.NoError(t, err)
	require.Equal(t, []GatewayDevshard{
		{ID: "11", Model: "m-a", Active: true, Resident: true, Nonce: 9},
		{ID: "12", Model: "m-b", Resident: true, Nonce: 4},
		{ID: "10", Model: "m-a"},
	}, devshards)
	throttle := collectThrottle(server.URL, "key")(context.Background(), "")
	require.Len(t, throttle, 2)
	require.True(t, throttle[0].Participants[0].inQuarantine())
	require.Equal(t, 3, throttle[1].Participants[0].FailureStrikes)
	pending := collectPending(server.URL, "key")(context.Background(), "")
	require.Len(t, pending, 2)
	require.Empty(t, pending[0].Error)
	require.Equal(t, 1, pending[0].PendingTxs)
	require.Equal(t, 1, pending[0].Open)
	require.InDelta(t, 60, pending[0].MaxAgeSeconds, 2)
	require.NotContains(t, paths, "/v1/admin/devshards/10/participants")
}
