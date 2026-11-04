package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"
	"devshard/testenv/mockopenai"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPoCScenarioAndHAPlanning(t *testing.T) {
	s, err := LoadScenario(filepath.Join("scenarios", "poc-preserved-load.yaml"))
	require.NoError(t, err)
	require.Equal(t, 16*time.Minute, s.Duration())
	cfg := &config.File{}
	configureParticipants(cfg, s.Topology.Participants)
	cfg.ApplyDefaults()
	for i := range cfg.Hosts {
		cfg.Hosts[i].Address = fmt.Sprintf("participant-%d", i)
	}
	planned, _, err := planEvents(s, cfg)
	require.NoError(t, err)
	require.Len(t, planned, 2)
	require.ElementsMatch(t, []string{"versiond-2", "versiond-3", "versiond-4"}, planned[0].pocHosts)
	require.ElementsMatch(t, []string{"versiond-0", "versiond-1", "versiond-3", "versiond-4"}, planned[1].pocHosts)
	require.Len(t, planned[0].pocNodes, 3)
	require.Len(t, planned[1].pocNodes, 4)
	require.NotEqual(t, planned[0].pocPreserved, planned[1].pocPreserved)
	require.Equal(t, []string{"test-model"}, planned[0].pocModels)
	for _, mutate := range []func(*Scenario){
		func(s *Scenario) { s.Gateway.PoCRequestMode = "off" },
		func(s *Scenario) { s.Workload.Events[0].Params["preserved"] = "versiond-9" },
		func(s *Scenario) { s.Topology.MockML.Nodes[0].Host = "" },
		func(s *Scenario) {
			s.Workload.Events = append(s.Workload.Events, Event{Action: "pause", Target: "versiond-4", At: "3m", Until: "3m10s"})
		},
	} {
		// Clone maps as well as slices before mutating.
		body, err := json.Marshal(s)
		require.NoError(t, err)
		var changed Scenario
		require.NoError(t, json.Unmarshal(body, &changed))
		mutate(&changed)
		require.Error(t, changed.Validate())
	}
}

func TestPoCWindowCancellationCleansAllHosts(t *testing.T) {
	f := &fakeEventCalls{}
	e := plannedEvent{Event: Event{Action: "poc_window", Params: map[string]any{"kind": "cpoc"}}, pocHosts: []string{"h1", "h2"}, pocNodes: []string{"ml1"}, pocPreserved: []string{"p0"}, pocModels: []string{"m"}}
	apply, revert := pocWindowSteps(e, f)
	require.NoError(t, apply(context.Background()))
	require.NoError(t, revert(context.Background()))
	calls := strings.Join(f.Calls(), "\n")
	require.Contains(t, calls, `ml_fault ml1 {"abort_active":true}`)
	require.Contains(t, calls, `"confirmation_poc_phase":"CONFIRMATION_POC_GENERATION"`)
	require.Contains(t, calls, `"preserved_participants":["p0"]`)
	require.Contains(t, calls, "challenge h1 false")
	require.Contains(t, calls, "challenge h2 false")
	require.NotContains(t, calls, "epoch_advance")
}

func TestPoCMeasurementRejectsLeakedMLAndMissingEvidence(t *testing.T) {
	in := InvariantInputs{Started: fixtureT0, Workload: 8 * time.Minute,
		Hosts:     []InvariantHost{{ID: "h0", Participant: "p0"}, {ID: "h1", Participant: "p1"}},
		NodeHosts: map[string]string{"ml0": "h0", "ml1": "h1"},
		Events:    []EventRecord{{ID: "poc", Action: "poc_window", Params: map[string]any{"preserved": "h0"}, PlannedStart: at(2 * time.Minute), PlannedEnd: at(4 * time.Minute), StartedAt: atPtr(2 * time.Minute), EndedAt: atPtr(4 * time.Minute)}},
		Requests:  []RequestResult{{StartedAt: at(3 * time.Minute), Outcome: "completed"}},
	}
	sample := func(offset time.Duration, n uint64, aborted uint64) DapiHostSample {
		return DapiHostSample{At: at(offset), Hosts: map[string]DapiHostRow{"h0": {Allocations: n}, "h1": {Allocations: 10}}, MLNodes: map[string]mockopenai.Stats{"ml0": {RequestsReceived: n}, "ml1": {RequestsReceived: 10, AbortedRequests: aborted}}}
	}
	early := sample(60*time.Second, 5, 0)
	early.MLNodes["ml1"] = mockopenai.Stats{RequestsReceived: 5}
	in.DapiHosts = []DapiHostSample{early, sample(110*time.Second, 10, 0), sample(140*time.Second, 15, 2), sample(220*time.Second, 30, 2)}
	checks, _ := evalPoCLoad(newInvariantContext(in))
	for _, check := range checks {
		require.True(t, check.Passed, "%s: %s", check.Name, check.Actual)
	}
	in.DapiHosts[3].MLNodes["ml1"] = mockopenai.Stats{RequestsReceived: 11, AbortedRequests: 2}
	checks, _ = evalPoCLoad(newInvariantContext(in))
	leaked := false
	for _, check := range checks {
		if strings.HasSuffix(check.Name, "p1.no_new_ml") {
			leaked = !check.Passed
		}
	}
	require.True(t, leaked)
	in.DapiHosts = nil
	checks, _ = evalPoCLoad(newInvariantContext(in))
	for _, check := range checks {
		if strings.HasSuffix(check.Name, "no_new_ml") {
			require.False(t, check.Passed)
		}
	}
}

func TestPoCReportDoesNotHideMissesOrResets(t *testing.T) {
	in := InvariantInputs{Started: fixtureT0, Workload: 8 * time.Minute, Hosts: []InvariantHost{{ID: "h", Participant: "p0"}}, Accounting: []AccountingSnapshot{accountingAt(10*time.Second, "a", nil), accountingAt(100*time.Second, "b", map[string]uint64{"p0": 7})}}
	row := pocWindowSummary(newInvariantContext(in), "baseline", at(0), at(2*time.Minute), "")
	require.NotNil(t, row.Participants[0].Misses)
	require.Equal(t, uint64(7), *row.Participants[0].Misses)
	require.Nil(t, row.Nonces)
	in.Accounting[1].EpochIndex++
	row = pocWindowSummary(newInvariantContext(in), "baseline", at(0), at(2*time.Minute), "")
	require.Nil(t, row.Participants[0].Misses)
}

func TestPoCPartialApplyRestoresHostsWithDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeEventCalls{fail: map[string]error{"challenge h2 true": errors.New("injected failure")}}
	f.hook = func(call string) {
		if call == "challenge h2 true" {
			cancel()
		}
	}
	event := plannedEvent{Event: Event{ID: "poc", Action: "poc_window", Params: map[string]any{}}, window: eventWindow{end: time.Second}, pocHosts: []string{"h1", "h2"}}
	record := executeEvent(ctx, time.Now(), event, f, f)
	require.Contains(t, record.Error, "injected failure")
	require.Contains(t, f.Calls(), "challenge h1 false")
	require.Contains(t, f.Calls(), "challenge h2 false")
	require.Contains(t, f.Calls(), "phase null")
	require.NotContains(t, record.Error, "context canceled")
}

func TestPoCAbortBaselineExcludesBoundaryPoll(t *testing.T) {
	in := InvariantInputs{Hosts: []InvariantHost{{ID: "h", Participant: "p"}}, NodeHosts: map[string]string{"ml": "h"}}
	sample := func(atTime time.Time, aborted uint64) DapiHostSample {
		return DapiHostSample{At: atTime, Hosts: map[string]DapiHostRow{"h": {}}, MLNodes: map[string]mockopenai.Stats{"ml": {AbortedRequests: aborted}}}
	}
	// This poll starts just before entry, but its ML HTTP read completes
	// after the event. Treating it as baseline loses the cancellation.
	in.DapiHosts = []DapiHostSample{sample(at(110*time.Second), 0), sample(at(120*time.Second).Add(-time.Millisecond), 5), sample(at(140*time.Second), 5), sample(at(200*time.Second), 5)}
	_, _, aborted := pocMLDeltas(newInvariantContext(in), "p", at(135*time.Second), at(220*time.Second), at(120*time.Second))
	require.NotNil(t, aborted)
	require.Equal(t, uint64(5), *aborted)
}

func TestPoCModeValidatedForOrdinaryAndDatasetScenarios(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "poc-preserved-load.yaml"))
	require.NoError(t, err)
	scenario.Gateway.PoCRequestMode = "invalid-mode"
	require.ErrorContains(t, scenario.Validate(), "gateway.poc_request_mode")
	require.ErrorContains(t, scenario.ValidateForDataset(), "gateway.poc_request_mode")
	body, err := yaml.Marshal(scenario)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	require.NoError(t, os.WriteFile(path, body, 0o644))
	_, err = LoadScenario(path)
	require.ErrorContains(t, err, "gateway.poc_request_mode")
	_, err = LoadDatasetScenario(path)
	require.ErrorContains(t, err, "gateway.poc_request_mode")
}
