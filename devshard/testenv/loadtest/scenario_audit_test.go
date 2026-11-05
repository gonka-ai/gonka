package loadtest

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"devshard/transport"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Set rpc_chats_per_min on a host and require the resolved env to be that many chats times transport.RPCWeightChat.
// 2. Require explicit env to survive the merge and a second DEVSHARD_RPC_MSGS_PER_MIN to fail validation.
func TestTopologyHostRPCChatsPerMin(t *testing.T) {
	host := TopologyHost{ID: "versiond-3", RPCChatsPerMin: 300, Env: map[string]string{"DEVSHARD_STORAGE_MODE": "sqlite"}}
	env := host.ResolvedEnv()
	require.Equal(t, strconv.Itoa(300*transport.RPCWeightChat), env["DEVSHARD_RPC_MSGS_PER_MIN"])
	require.Equal(t, "sqlite", env["DEVSHARD_STORAGE_MODE"])
	require.NotContains(t, host.Env, "DEVSHARD_RPC_MSGS_PER_MIN", "the scenario value is not mutated")

	s := eventScenario()
	s.Topology.Hosts = []TopologyHost{host}
	require.NoError(t, s.Validate())
	s.Topology.Hosts[0].Env = map[string]string{"DEVSHARD_RPC_MSGS_PER_MIN": "60"}
	require.ErrorContains(t, s.Validate(), "rpc_chats_per_min")
	s.Topology.Hosts[0] = TopologyHost{ID: "versiond-3", RPCChatsPerMin: -1}
	require.ErrorContains(t, s.Validate(), "rpc_chats_per_min")
}

// Test flow:
// 1. Overlap a background marker with a pause on the same host and require validation to pass.
// 2. Require a plain marker in the same place, or background on a non-marker action, to fail.
func TestBackgroundMarkerValidation(t *testing.T) {
	pause := Event{ID: "pause", Case: "receipt-then-empty-stream", Action: "pause", Target: "versiond-2", At: "1m", Until: "2m"}
	marker := Event{ID: "budget", Case: "receipt-then-empty-stream", Action: "marker", Target: "versiond-2", At: "0s", Until: "10m",
		Params: map[string]any{"background": true, "observe": "host_rate_limited"}}
	s := eventScenario()
	s.Workload.Events = []Event{pause, marker}
	require.NoError(t, s.Validate())

	plain := marker
	plain.Params = map[string]any{}
	s.Workload.Events = []Event{pause, plain}
	require.ErrorContains(t, s.Validate(), "overlap")

	bad := pause
	bad.Params = map[string]any{"background": true}
	s.Workload.Events = []Event{bad}
	require.ErrorContains(t, s.Validate(), "background")

	unknown := marker
	unknown.Params = map[string]any{"observe": "nothing"}
	s.Workload.Events = []Event{unknown}
	require.ErrorContains(t, s.Validate(), "observe")
}

// Test flow:
// 1. Parse a host /metrics body with request terminals, peer budget waits and an unrelated series.
// 2. Require the terminal and budget series kept and the unrelated one dropped.
func TestParseDomainMetricsKeepsBudgetSeries(t *testing.T) {
	body := `devshard_request_terminal_total{reason="rate_limited",terminal="no_receipt_interrupted"} 4
devshard_peer_rpc_budget_wait_total{endpoint="GetDiffs"} 2
devshard_peer_rpc_budget_wait_skipped_total{endpoint="Chat"} 1
devshard_inflight{stage="execute"} 3
`
	values := parseDomainMetrics(body)
	require.Equal(t, 4.0, values[hostRateLimitedKey])
	require.Equal(t, 2.0, values[`devshard_peer_rpc_budget_wait_total{endpoint="GetDiffs"}`])
	require.Equal(t, 1.0, values[`devshard_peer_rpc_budget_wait_skipped_total{endpoint="Chat"}`])
	require.NotContains(t, values, `devshard_inflight{stage="execute"}`)
}

// descriptiveName is a lowercase-hyphen name whose first word is letters only.
var descriptiveName = regexp.MustCompile(`^[a-z]{2,}(-[a-z0-9]+)+$`)

// Test flow:
// 1. Load every scenario that declares cases and require each case id, event id, event case and invariant case key to be a descriptive name.
// 2. Evaluate the invariants on empty inputs and require every check's invariant name to be descriptive.
func TestScenarioAndCheckNamesAreDescriptive(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("scenarios", "*.yaml"))
	require.NoError(t, err)
	for _, path := range paths {
		s, err := LoadScenario(path)
		require.NoError(t, err, path)
		if len(s.Cases) == 0 {
			continue
		}
		for _, c := range s.Cases {
			require.Regexp(t, descriptiveName, c.ID, path)
		}
		for _, event := range s.Workload.Events {
			require.Regexp(t, descriptiveName, event.ID, path)
			require.Regexp(t, descriptiveName, event.Case, path)
		}
		for name := range s.Assertions.Invariants.Cases {
			invariant, _, _ := strings.Cut(name, ".")
			require.Regexp(t, descriptiveName, invariant, path)
		}
	}
	checks, _ := evaluateInvariants(InvariantInputs{Settings: invariantSettings{Cases: map[string]string{}}})
	require.NotEmpty(t, checks)
	for _, check := range checks {
		parts := strings.SplitN(check.Name, ".", 3)
		require.Len(t, parts, 3, check.Name)
		require.Regexp(t, descriptiveName, parts[1], check.Name)
	}
}
