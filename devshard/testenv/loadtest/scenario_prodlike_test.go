package loadtest

import (
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"devshard/transport"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Load scenarios/prod-like-15m.yaml through LoadScenario, which runs the full validation including event overlap rules.
// 2. Check the topology, protocol, traffic stages, request mix, models, per-host budget from RPCWeightChat and sqlite host match the design.
// 3. Check the known_bug markers and the run-level and per-check invariant bindings.
func TestLoadScenario_ProdLike15m(t *testing.T) {
	s, err := LoadScenario(filepath.Join("scenarios", "prod-like-15m.yaml"))
	require.NoError(t, err)
	require.Equal(t, 5, s.Topology.Participants)
	require.Len(t, s.Topology.MockML.Nodes, 6)
	for _, node := range s.Topology.MockML.Nodes {
		require.NotEmpty(t, node.Host, node.Name)
		require.Equal(t, "prod-like", node.Profile)
	}
	require.Equal(t, "5s", s.Protocol.RefusalTimeout)
	require.Equal(t, "30s", s.Protocol.ExecutionTimeout)
	require.Equal(t, 15*time.Minute, s.Duration())
	require.Equal(t, 256, s.Workload.MaxInFlight)
	var stages []string
	for _, stage := range s.Workload.Traffic.Stages {
		stages = append(stages, stage.Duration+"@"+strconv.FormatFloat(s.Workload.Traffic.stageRPS(stage), 'f', -1, 64))
	}
	require.Equal(t, []string{"1m@8", "1m@16", "8m@16", "2m@16", "3m@16"}, stages)
	require.True(t, s.Workload.Traffic.Stages[3].Peak)
	require.Equal(t, float64(16), s.Workload.Traffic.PeakRPS)
	require.Equal(t, 0.12, s.Assertions.Requests.ErrorRate)
	mix := s.Workload.Request.Mix
	require.NotNil(t, mix)
	require.Equal(t, 0.5, mix.StreamRatio)
	require.Equal(t, 0.5, mix.LogprobsRatio)
	require.Equal(t, 5, mix.TopLogprobs)
	require.Len(t, mix.Prompts, 3)
	require.Equal(t, []Weighted[string]{{Value: "model-a", Weight: 70}, {Value: "model-b", Weight: 30}}, mix.Models)
	require.Len(t, s.Gateway.EscrowRotation.Models, 1)
	require.Equal(t, "model-b", s.Gateway.EscrowRotation.Models[0].Model)
	require.False(t, *s.Gateway.EscrowRotation.Models[0].SettlementEnabled)
	require.True(t, s.Gateway.EscrowRotation.SettlementEnabled)
	require.Equal(t, "versiond-5", s.Topology.Hosts[0].ID)
	require.Equal(t, 300, s.Topology.Hosts[0].RPCChatsPerMin)
	require.Equal(t, strconv.Itoa(300*transport.RPCWeightChat), s.Topology.Hosts[0].ResolvedEnv()["DEVSHARD_RPC_MSGS_PER_MIN"])
	require.Equal(t, uint32(1700), s.Topology.Chain.MaxNonce)
	require.Equal(t, []string{"versiond-4"}, zstdPayloadHosts(s))
	for _, quarantine := range []string{s.Gateway.ParticipantThrottle.HTTPQuarantine, s.Gateway.ParticipantThrottle.TransportFailureQuarantine, s.Gateway.ParticipantThrottle.EmptyStreamQuarantine, s.Gateway.ParticipantThrottle.StalledWinnerQuarantine} {
		require.Equal(t, "45s", quarantine)
	}

	bugs := map[string]string{}
	for _, c := range s.Cases {
		if c.KnownBug != "" {
			bugs[c.ID] = c.KnownBug
		}
	}
	require.Equal(t, map[string]string{"host-over-peer-chat-budget": "#1988", "host-under-poc-challenge": "#1823", "deactivated-escrow-runtime-retired": "#1988", "timeout-vote-fanout": "#1988", "session-nonce-within-max-nonce": "nonce-overrun"}, bugs)
	require.True(t, s.Assertions.Invariants.Enabled)
	require.Equal(t, map[string]string{"escrow-accounting-reconciles.final_nonce_le_max": "session-nonce-within-max-nonce", "no-runtime-for-deactivated-escrow": "deactivated-escrow-runtime-retired", "responses-compress": "logprobs-responses-compress", "payload-files-are-zstd": "sqlite-payloads-in-zstd-files", "no-http-5xx-on-honest-hosts": "honest-hosts-get-no-http-5xx", "no-escrow-falsely-marked-missing": "gateway-restart-while-participant-absent"}, s.Assertions.Invariants.Cases)
}

// Test flow:
// 1. Load the prod-like-15m scenario and resolve every event to its host lane and window.
// 2. Check P0 and P1 (versiond-0, versiond-1, versiond-2 and their ML nodes) are never targeted.
// 3. Check consecutive cases on one host leave at least 30 s of quiet, and global events stay outside the peak stage.
// 4. Check every declared case except the whole-run ones has an event, and every event names a case.
// 5. Check the host-over-peer-chat-budget background marker spans the workload and the gateway-restart-while-participant-absent restart falls inside participant-absent-on-chain.
func TestProdLike15mTimelineRules(t *testing.T) {
	s, err := LoadScenario(filepath.Join("scenarios", "prod-like-15m.yaml"))
	require.NoError(t, err)
	nodeHosts := map[string]string{}
	for _, node := range s.Topology.MockML.Nodes {
		nodeHosts[node.Name] = node.Host
	}
	type laneEvent struct {
		id, caseID string
		window     eventWindow
	}
	lanes := map[string][]laneEvent{}
	windows := map[string]eventWindow{}
	withEvents := map[string]bool{}
	for _, event := range s.TimelineEvents() {
		require.NotEmpty(t, event.Case, event.ID)
		withEvents[event.Case] = true
		window, err := parseEventWindow(event, eventActions[event.Action], s.Duration())
		require.NoError(t, err)
		lane := eventLane(event, nodeHosts)
		require.NotContains(t, []string{"versiond-0", "versiond-1", "versiond-2"}, lane, event.ID)
		windows[event.ID] = window
		if isBackground(event.Params) {
			continue
		}
		if lane == "global" {
			require.False(t, window.overlaps(eventWindow{start: 10 * time.Minute, end: 12 * time.Minute}), event.ID)
		}
		lanes[lane] = append(lanes[lane], laneEvent{event.ID, event.Case, window})
	}
	for lane, events := range lanes {
		sort.Slice(events, func(i, j int) bool { return events[i].window.start < events[j].window.start })
		for i := 1; i < len(events); i++ {
			if events[i].caseID == events[i-1].caseID {
				continue
			}
			gap := events[i].window.start - max(events[i-1].window.end, events[i-1].window.start)
			require.GreaterOrEqual(t, gap, 30*time.Second, "%s: %s after %s", lane, events[i].id, events[i-1].id)
		}
	}
	for _, c := range s.Cases {
		switch c.ID {
		case "escrow-rotation-per-model-settlement", "deactivated-escrow-runtime-retired", "logprobs-responses-compress", "sqlite-payloads-in-zstd-files", "session-nonce-within-max-nonce", "honest-hosts-get-no-http-5xx":
			require.False(t, withEvents[c.ID], c.ID)
		default:
			require.True(t, withEvents[c.ID], c.ID)
		}
	}
	require.Equal(t, eventWindow{start: 0, end: s.Duration()}, windows["host-over-peer-chat-budget"])
	absent, restart := windows["participant-absent-on-chain"], windows["gateway-restart-while-participant-absent"]
	require.True(t, restart.start > absent.start && restart.start < absent.end)
}
