package loadtest

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"devshard/transport"

	"github.com/stretchr/testify/require"
)

var splitScenarios = []string{"prod-like-steady", "faults-ml-node", "faults-host", "faults-gateway-restart", "epoch-change", "vote-fanout", "load-ladder"}

const plannedEventDrift = 30 * time.Second

const (
	p4ChatsPerRPS            = 3.8 / 16
	gatewayPacingFloorPerMin = 10
)

var nestedFaults = map[string]string{"faults-gateway-restart/gateway-restart-while-participant-absent": "participant-absent-on-chain"}

type plannedFault struct {
	id, action, participant string
	start, end              time.Duration
}

func loadSplitScenario(t *testing.T, name string) Scenario {
	t.Helper()
	s, err := LoadScenario(filepath.Join("scenarios", name+".yaml"))
	require.NoError(t, err, name)
	return s
}

func plannedFaults(t *testing.T, s Scenario) []plannedFault {
	t.Helper()
	participants := map[string]string{"versiond-0": "P0", "versiond-1": "P0", "versiond-2": "P1", "versiond-3": "P2", "versiond-4": "P3", "versiond-5": "P4"}
	nodeHosts := map[string]string{}
	for _, node := range s.Topology.MockML.Nodes {
		nodeHosts[node.Name] = node.Host
	}
	var faults []plannedFault
	for _, event := range s.TimelineEvents() {
		if isBackground(event.Params) {
			continue
		}
		window, err := parseEventWindow(event, eventActions[event.Action], s.Duration())
		require.NoError(t, err)
		lane := eventLane(event, nodeHosts)
		faults = append(faults, plannedFault{id: event.ID, action: event.Action, participant: participants[lane], start: window.start, end: max(window.end, window.start)})
	}
	sort.Slice(faults, func(i, j int) bool { return faults[i].start < faults[j].start })
	return faults
}

func eventLength(t *testing.T, event Event) time.Duration {
	t.Helper()
	if event.Until == "" {
		return 0
	}
	start, err := time.ParseDuration(event.At)
	require.NoError(t, err)
	end, err := time.ParseDuration(event.Until)
	require.NoError(t, err)
	return end - start
}

// Test flow:
// 1. List every scenarios/*.yaml file, and require each split scenario among them.
// 2. Load each file through LoadScenario, which runs the full validation, including the event overlap rules.
// 3. Require each scenario to be named after its file.
func TestLoadEveryScenarioFile(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("scenarios", "*.yaml"))
	require.NoError(t, err)
	for _, name := range splitScenarios {
		require.Contains(t, paths, filepath.Join("scenarios", name+".yaml"))
	}

	for _, path := range paths {
		s, err := LoadScenario(path)

		require.NoError(t, err, path)
		require.Equal(t, strings.TrimSuffix(filepath.Base(path), ".yaml"), s.Scenario, path)
	}
}

// Test flow:
// 1. Load each split scenario and lay out its planned faults, background markers left out.
// 2. For each fault, take its recovery-after-fault recovery window: the planned end, plus the 60 s budget (90 s after a gateway restart), one 30 s bucket and 30 s of start-stamp drift.
// 3. Find the next fault the way recovery-after-fault does: the first other fault still active at or after that end, or the workload end.
// 4. Require the recovery window to end before it, except for a fault nested by design, which must lie inside its host fault.
func TestSplitScenariosJudgeEachRecoveryBeforeTheNextFault(t *testing.T) {
	for _, name := range splitScenarios {
		faults := plannedFaults(t, loadSplitScenario(t, name))
		s := loadSplitScenario(t, name)
		for _, fault := range faults {
			if host, nested := nestedFaults[name+"/"+fault.id]; nested {
				var outer plannedFault
				for _, other := range faults {
					if other.id == host {
						outer = other
					}
				}
				require.True(t, fault.start > outer.start && fault.end < outer.end, "%s: %s inside %s", name, fault.id, host)
				continue
			}
			limit := recoveryLimit
			if fault.action == "gateway_restart" {
				limit = restartRecoveryLim
			}
			recoveryEnd := fault.end + limit + recoveryBucket + plannedEventDrift
			next, nextID := s.Duration(), "the workload end"
			for _, other := range faults {
				if other.id == fault.id || other.end < fault.end {
					continue
				}
				if start := max(other.start, fault.end); start < next {
					next, nextID = start, other.id
				}
			}

			require.LessOrEqual(t, recoveryEnd, next, "%s: %s recovery ends at %s, %s starts at %s", name, fault.id, recoveryEnd, nextID, next)
		}
	}
}

// Test flow:
// 1. Load each split scenario and lay out its planned faults in start order, the nested one left out.
// 2. Require at least 2 minutes and one request timeout between consecutive faults, and the next fault to end at least the grace plus 15 s after the previous one, so no request start falls in two graced tails.
// 3. Require two faults on one participant to be the grace plus a quarantine and a poll apart, and P0 and P1 never targeted.
// 4. Require the workload to run the last fault's whole grace tail.
func TestSplitScenariosKeepFaultsApart(t *testing.T) {
	for _, name := range splitScenarios {
		s := loadSplitScenario(t, name)
		grace := resolveInvariantSettings(s).Grace
		quarantine, err := time.ParseDuration(s.Gateway.ParticipantThrottle.StalledWinnerQuarantine)
		require.NoError(t, err)
		var faults []plannedFault
		for _, fault := range plannedFaults(t, s) {
			if _, nested := nestedFaults[name+"/"+fault.id]; !nested {
				faults = append(faults, fault)
			}
		}
		lastOn := map[string]plannedFault{}
		for i, fault := range faults {
			require.NotContains(t, []string{"P0", "P1"}, fault.participant, "%s: %s", name, fault.id)
			if i > 0 {
				previous := faults[i-1]
				gap := fault.start - previous.end
				require.GreaterOrEqual(t, gap, 2*time.Minute, "%s: %s after %s", name, fault.id, previous.id)
				require.GreaterOrEqual(t, gap, s.RequestTimeout(), "%s: %s after %s", name, fault.id, previous.id)
				require.GreaterOrEqual(t, fault.end-previous.end, grace+15*time.Second, "%s: %s after %s", name, fault.id, previous.id)
			}
			if earlier, seen := lastOn[fault.participant]; seen && fault.participant != "" {
				require.GreaterOrEqual(t, fault.start-earlier.end, grace+quarantine+quarantineDurationT, "%s: %s after %s on %s", name, fault.id, earlier.id, fault.participant)
			}
			lastOn[fault.participant] = fault
		}
		if len(faults) > 0 {
			require.GreaterOrEqual(t, s.Duration()-faults[len(faults)-1].end, grace, name)
		}
	}
}

// Test flow:
// 1. Load prod-like-15m and every split scenario.
// 2. Require seed 1515, prod-like's protocol (its 90 s execution timeout included), prod-like's gateway, its topology without P4's peer budget, or in prod-like-steady with P4's lower peer chat budget (host-over-peer-chat-budget), and its request mix except the load ladder's max_tokens weights.
// 3. Require every event to copy a prod-like event's case, action, target and params, with its length unchanged except participant-absent-on-chain's, and every prod-like event to land in exactly one split scenario.
// 4. Require every declared case to match prod-like's title; TestSplitScenarioKnownBugMarkers pins the known_bug markers.
func TestSplitScenariosCopyProdLikeFaults(t *testing.T) {
	prod := loadSplitScenario(t, "prod-like-15m")
	events, cases := map[string]Event{}, map[string]Case{}
	for _, event := range prod.Workload.Events {
		events[event.ID] = event
	}
	for _, c := range prod.Cases {
		cases[c.ID] = c
	}
	withoutBudget := prod.Topology
	withoutBudget.Hosts = nil
	for _, host := range prod.Topology.Hosts {
		if host.RPCChatsPerMin == 0 {
			withoutBudget.Hosts = append(withoutBudget.Hosts, host)
		}
	}
	lengths := map[string]time.Duration{"participant-absent-on-chain": 45 * time.Second}
	placed := map[string]string{}

	for _, name := range splitScenarios {
		s := loadSplitScenario(t, name)
		require.Equal(t, int64(1515), s.Seed, name)
		require.Equal(t, prod.Protocol, s.Protocol, name)
		require.Equal(t, prod.Gateway, s.Gateway, name)
		if name == "prod-like-steady" {
			withSteadyBudget := prod.Topology
			withSteadyBudget.Hosts = append([]TopologyHost(nil), prod.Topology.Hosts...)
			for i, host := range withSteadyBudget.Hosts {
				if host.ID == "versiond-5" {
					withSteadyBudget.Hosts[i].RPCChatsPerMin = s.Topology.Hosts[i].RPCChatsPerMin
				}
			}
			require.Equal(t, withSteadyBudget, s.Topology, name)
		} else {
			require.Equal(t, withoutBudget, s.Topology, name)
		}
		request := s.Workload.Request
		if name == "load-ladder" {
			require.Equal(t, []Weighted[int]{{Value: 32, Weight: 40}, {Value: 128, Weight: 35}, {Value: 512, Weight: 22}, {Value: 2048, Weight: 3}}, request.Mix.MaxTokens)
			mix := *request.Mix
			mix.MaxTokens = prod.Workload.Request.Mix.MaxTokens
			request.Mix = &mix
		}
		require.Equal(t, prod.Workload.Request, request, name)
		for _, event := range s.Workload.Events {
			original, found := events[event.ID]
			require.True(t, found, "%s: %s", name, event.ID)
			require.Empty(t, placed[event.ID], "%s: %s already in %s", name, event.ID, placed[event.ID])
			placed[event.ID] = name
			require.Equal(t, original.Case, event.Case, event.ID)
			require.Equal(t, original.Action, event.Action, event.ID)
			require.Equal(t, original.Target, event.Target, event.ID)
			require.Equal(t, original.Params, event.Params, event.ID)
			length, stretched := lengths[event.ID]
			if !stretched {
				length = eventLength(t, original)
			}
			require.Equal(t, length, eventLength(t, event), event.ID)
		}
		for _, c := range s.Cases {
			require.Equal(t, cases[c.ID].Title, c.Title, "%s: %s", name, c.ID)
		}
	}
	for id := range events {
		require.NotEmpty(t, placed[id], id)
	}
}

// Test flow:
// 1. Load the load ladder.
// 2. Require 2 minutes each at 8, 16, 24 and 32 rps, then 2 minutes back at 16, and a 10-minute workload.
// 3. Require max_in_flight 1024, about 4x the in-flight count at 32 rps, and the late trend window inside the cool-down step.
func TestLoadLadderPeaksAt32RPS(t *testing.T) {
	s := loadSplitScenario(t, "load-ladder")
	var stages []string
	for _, stage := range s.Workload.Traffic.Stages {
		stages = append(stages, stage.Duration+"@"+strconv.FormatFloat(s.Workload.Traffic.stageRPS(stage), 'f', -1, 64))
	}

	require.Equal(t, []string{"2m@8", "2m@16", "2m@24", "2m@32", "2m@16"}, stages)
	require.Equal(t, 10*time.Minute, s.Duration())
	require.Equal(t, 1024, s.Workload.MaxInFlight)
	require.Equal(t, [2]time.Duration{8*time.Minute + 30*time.Second, 10 * time.Minute}, resolveInvariantSettings(s).TrendLate)
}

// Test flow:
// 1. Load prod-like-steady and resolve P4's (versiond-5) budget to DEVSHARD_RPC_MSGS_PER_MIN, and the host-over-peer-chat-budget marker's window.
// 2. Require the budget below the gateway's client pacing floor (10 a minute, transport/rpc_budget.go), so the gateway neither paces nor skips P4 and P4 itself refuses.
// 3. For every traffic stage in the window, plan P4's Chats a second as the stage rps times batch A's lowest P4 share (3.8 Chats/s at 16 rps).
// 4. Require each planned rate above the budget in Chats a second, so P4 is over budget the whole window.
func TestProdLikeSteadyDrivesP4OverItsBudget(t *testing.T) {
	s := loadSplitScenario(t, "prod-like-steady")
	budget := 0
	for _, host := range s.Topology.Hosts {
		if host.ID == "versiond-5" {
			var err error
			budget, err = strconv.Atoi(host.ResolvedEnv()[rpcMsgsPerMinEnv])
			require.NoError(t, err)
		}
	}
	var window eventWindow
	for _, event := range s.Workload.Events {
		if event.ID == "host-over-peer-chat-budget" {
			var err error
			window, err = parseEventWindow(event, eventActions[event.Action], s.Duration())
			require.NoError(t, err)
		}
	}
	require.Equal(t, eventWindow{start: 0, end: s.Duration()}, window)
	require.Positive(t, budget)
	require.Less(t, budget, gatewayPacingFloorPerMin)
	budgetChats := float64(budget) / float64(transport.RPCWeightChat) / 60

	var stageStart time.Duration
	for _, stage := range s.Workload.Traffic.Stages {
		length, err := time.ParseDuration(stage.Duration)
		require.NoError(t, err)
		if window.overlaps(eventWindow{start: stageStart, end: stageStart + length}) {
			planned := s.Workload.Traffic.stageRPS(stage) * p4ChatsPerRPS
			require.Greater(t, planned, budgetChats, "stage at %s: P4 planned %.2f Chats/s, budget %.2f", stageStart, planned, budgetChats)
		}
		stageStart += length
	}
	require.Equal(t, s.Duration(), stageStart)
}

// Test flow:
// 1. Load every split scenario and collect its known_bug markers.
// 2. Require host-over-peer-chat-budget (#1988) only in prod-like-steady, host-under-poc-challenge (#1823) only in faults-host, timeout-vote-fanout (#1988) only in vote-fanout, and session-nonce-within-max-nonce in each.
// 3. Require no deactivated-escrow-runtime-retired marker: #1988 retires a non-settling model's runtime (gateway.go:5102-5104), and every v6 run passed no-runtime-for-deactivated-escrow.
func TestSplitScenarioKnownBugMarkers(t *testing.T) {
	want := map[string]map[string]string{
		"prod-like-steady":       {"host-over-peer-chat-budget": "#1988", "session-nonce-within-max-nonce": "nonce-overrun"},
		"faults-ml-node":         {"session-nonce-within-max-nonce": "nonce-overrun"},
		"faults-host":            {"host-under-poc-challenge": "#1823", "session-nonce-within-max-nonce": "nonce-overrun"},
		"faults-gateway-restart": {"session-nonce-within-max-nonce": "nonce-overrun"},
		"epoch-change":           {"session-nonce-within-max-nonce": "nonce-overrun"},
		"vote-fanout":            {"timeout-vote-fanout": "#1988", "session-nonce-within-max-nonce": "nonce-overrun"},
		"load-ladder":            {"session-nonce-within-max-nonce": "nonce-overrun"},
	}
	for _, name := range splitScenarios {
		bugs := map[string]string{}
		for _, c := range loadSplitScenario(t, name).Cases {
			if c.KnownBug != "" {
				bugs[c.ID] = c.KnownBug
			}
		}

		require.Equal(t, want[name], bugs, name)
	}
}

const generationTailTimeout = 90 * time.Second

var executionTimeoutScenarios = map[string]string{
	"missrate-catch-up": "requires an applied timeout from a 30 s pause",
}

// promiseScenarios are judged by the promise checks, not the invariants, and run ML nodes whose every
// generation ends well inside the execution timeout: a short timeout cuts no honest host, and the
// invariants' grace does not apply to them.
var promiseScenarios = map[string]bool{
	"chunk-shapes": true, "phase-faults": true, "drain": true, "shared-endpoint": true,
	"dishonest-executor-honest": true, "dishonest-executor-overclaim": true, "dishonest-executor-wrong-answer": true,
	"usage-challenge-wave": true, "usage-challenge-wave-honest": true,
}

// hangScenarios hold nonces to the execution deadline, so they keep the default grace.
var hangScenarios = map[string]string{
	"vote-fanout":   "timeout-vote-fanout: hosts vote the nonces of a hang that outlasts the timeout",
	"prod-like-15m": "carries timeout-vote-fanout",
}

// Test flow:
// 1. Load every scenarios/*.yaml file that sets protocol.execution_timeout.
// 2. Require the scenarios that test execution timeouts to keep a timeout below the generation tail, with a reason.
// 3. Require every other one to set at least 90 s, above the longest generation, so no honest host is cut at its deadline.
// 4. Require the hang scenarios to keep the default grace and to hang longer than refusal plus execution timeout, so the timeout fires inside the hang.
// 5. Require every other one to pin the grace at 3m10s, so its fault spacing still holds.
// 6. Leave the promise scenarios out of 3 to 5: they judge no invariants, and their nodes finish every generation inside the timeout.
func TestScenariosSetExecutionTimeoutAboveTheGenerationTail(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("scenarios", "*.yaml"))
	require.NoError(t, err)
	for _, names := range []map[string]string{executionTimeoutScenarios, hangScenarios} {
		for name := range names {
			require.Contains(t, paths, filepath.Join("scenarios", name+".yaml"))
		}
	}
	for name := range promiseScenarios {
		require.Contains(t, paths, filepath.Join("scenarios", name+".yaml"))
	}
	for _, path := range paths {
		s, err := LoadScenario(path)
		require.NoError(t, err, path)
		if s.Protocol.ExecutionTimeout == "" {
			continue
		}
		execution, err := time.ParseDuration(s.Protocol.ExecutionTimeout)
		require.NoError(t, err, path)

		if promiseScenarios[s.Scenario] {
			require.True(t, s.Assertions.Promises, s.Scenario)
			require.False(t, s.Assertions.Invariants.Enabled, s.Scenario)
			for _, node := range s.Topology.MockML.Nodes {
				profile, err := LoadProfile("profiles", node.Profile)
				require.NoError(t, err, path)
				require.Less(t, time.Duration(max(profile.FirstContentMs, profile.ExecMs))*time.Millisecond, execution, "%s: %s", s.Scenario, node.Profile)
			}
			continue
		}
		if reason, testsTimeouts := executionTimeoutScenarios[s.Scenario]; testsTimeouts {
			require.Less(t, execution, generationTailTimeout, "%s: %s", s.Scenario, reason)
			continue
		}
		require.GreaterOrEqual(t, execution, generationTailTimeout, s.Scenario)
		if reason, hangs := hangScenarios[s.Scenario]; hangs {
			refusal, err := time.ParseDuration(s.Protocol.RefusalTimeout)
			require.NoError(t, err, path)
			require.Empty(t, s.Assertions.Invariants.Grace, "%s: %s", s.Scenario, reason)
			var hangs []eventWindow
			for _, event := range s.Workload.Events {
				if event.ID == "timeout-vote-fanout" {
					window, err := parseEventWindow(event, eventActions[event.Action], s.Duration())
					require.NoError(t, err, path)
					hangs = append(hangs, window)
				}
			}
			require.Len(t, hangs, 1, s.Scenario)
			require.Greater(t, hangs[0].end-hangs[0].start, refusal+execution, "%s: %s", s.Scenario, reason)
			continue
		}
		require.Equal(t, 3*time.Minute+10*time.Second, resolveInvariantSettings(s).Grace, s.Scenario)
	}
}
