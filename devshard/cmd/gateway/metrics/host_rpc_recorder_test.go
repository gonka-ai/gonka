package metrics

import (
	"testing"

	"devshard/transport"
)

func answeredSnapshot() *transport.RPCStatsSnapshot {
	return &transport.RPCStatsSnapshot{
		MinuteUnix: 1_700_000_040,
		Partial:    true,
		Host: transport.RPCStatsHost{
			Requests: 100,
			Banned:   9,
			Endpoints: []transport.RPCStatsEndpoint{
				{Endpoint: "Chat", Zone: "shared", Requests: 60, Banned: 4},
				{Endpoint: "Chat", Zone: "streams", Requests: 10, Banned: 1},
				{Endpoint: "MadeUpByTheHost", Requests: 5, Banned: 1},
				{Endpoint: "AlsoMadeUp", Requests: 2, Banned: 0},
			},
			Zones: []transport.RPCStatsZone{
				{Zone: "shared", Requests: 80, Banned: 6},
				{Zone: "attach_floor", Requests: 12, Banned: 3},
				{Zone: "invented", Requests: 8, Banned: 0},
			},
			Reconnects: []transport.RPCStatsReconnect{
				{Peer: "peer-a", Reason: "watch", Attempts: 2},
				{Peer: "peer-b", Reason: "watch", Attempts: 3},
				{Peer: "peer-b", Reason: "surprise", Attempts: 4},
			},
			Attach: transport.RPCStatsAttach{Attempts: 20, Banned: 7, BannedFloor: 3, BannedOrigin: 2},
		},
		Shards: []transport.RPCStatsShard{{EscrowID: "42", Requests: 5, Banned: 1}},
	}
}

// Test flow:
//  1. Build a recorder and give it one host that answered with endpoints, zones and reconnects the host named freely, and one that did not.
//  2. Gather the registry.
//  3. Assert the answering host's totals, endpoint and zone rows summed with unknown names under `other`, attach refusals split into floor, origin and other, and reconnects summed over peers.
//  4. Assert the silent host exports `stats_up` 0 and nothing else.
func TestTheHostRPCRecorderExportsWhatTheHostReportedUnderBoundedLabels(t *testing.T) {
	telemetry := New()
	recorder := NewHostRPCRecorder(telemetry)

	recorder.Replace(map[string]*transport.RPCStatsSnapshot{"gonka1up": answeredSnapshot(), "gonka1down": nil})

	up := labels{"participant_key": "gonka1up"}
	expectGauge(t, telemetry, "devshard_gateway_rpc_stats_up", up, 1)
	expectGauge(t, telemetry, "devshard_gateway_rpc_stats_up", labels{"participant_key": "gonka1down"}, 0)
	expectGauge(t, telemetry, "devshard_gateway_rpc_requests_last_minute", up, 100)
	expectGauge(t, telemetry, "devshard_gateway_rpc_banned_last_minute", up, 9)
	expectGauge(t, telemetry, "devshard_gateway_rpc_minute_unix", up, 1_700_000_040)
	expectGauge(t, telemetry, "devshard_gateway_rpc_stats_partial", up, 1)
	expectGauge(t, telemetry, "devshard_gateway_rpc_endpoint_requests_last_minute", labels{"participant_key": "gonka1up", "endpoint": "Chat"}, 70)
	expectGauge(t, telemetry, "devshard_gateway_rpc_endpoint_banned_last_minute", labels{"participant_key": "gonka1up", "endpoint": "Chat"}, 5)
	expectGauge(t, telemetry, "devshard_gateway_rpc_endpoint_requests_last_minute", labels{"participant_key": "gonka1up", "endpoint": "other"}, 7)
	expectGauge(t, telemetry, "devshard_gateway_rpc_endpoint_banned_last_minute", labels{"participant_key": "gonka1up", "endpoint": "other"}, 1)
	expectGauge(t, telemetry, "devshard_gateway_rpc_zone_banned_last_minute", labels{"participant_key": "gonka1up", "zone": "attach_floor"}, 3)
	expectGauge(t, telemetry, "devshard_gateway_rpc_zone_requests_last_minute", labels{"participant_key": "gonka1up", "zone": "other"}, 8)
	expectGauge(t, telemetry, "devshard_gateway_rpc_attach_attempts_last_minute", up, 20)
	expectGauge(t, telemetry, "devshard_gateway_rpc_attach_banned_last_minute", labels{"participant_key": "gonka1up", "reason": "floor"}, 3)
	expectGauge(t, telemetry, "devshard_gateway_rpc_attach_banned_last_minute", labels{"participant_key": "gonka1up", "reason": "origin"}, 2)
	expectGauge(t, telemetry, "devshard_gateway_rpc_attach_banned_last_minute", labels{"participant_key": "gonka1up", "reason": "other"}, 2)
	expectGauge(t, telemetry, "devshard_gateway_rpc_reconnects_last_minute", labels{"participant_key": "gonka1up", "reason": "watch"}, 5)
	expectGauge(t, telemetry, "devshard_gateway_rpc_reconnects_last_minute", labels{"participant_key": "gonka1up", "reason": "other"}, 4)
	if got := familiesCarrying(t, telemetry, "participant_key", "gonka1down"); got != 1 {
		t.Errorf("families carrying the silent host = %d, want only stats_up", got)
	}
}

// Test flow:
//  1. Build a recorder and give it a host with an empty participant key, and assert it is labelled `unknown`.
//  2. Replace the wave with keys `""`, `"unknown"` and `" "`, which all take the label `unknown`.
//  3. Assert the registry still gathers, so one label never carries two series, and that the smallest key, which answered, is the one exported.
//  4. Replace the wave with an empty one and assert no host series remain.
func TestTheHostRPCRecorderKeepsLabelsNonEmptyAndForgetsHostsThatLeft(t *testing.T) {
	telemetry := New()
	recorder := NewHostRPCRecorder(telemetry)

	recorder.Replace(map[string]*transport.RPCStatsSnapshot{"": answeredSnapshot()})
	expectGauge(t, telemetry, "devshard_gateway_rpc_requests_last_minute", labels{"participant_key": "unknown"}, 100)

	recorder.Replace(map[string]*transport.RPCStatsSnapshot{"": answeredSnapshot(), "unknown": answeredSnapshot(), " ": nil})
	if _, err := telemetry.Registry().Gather(); err != nil {
		t.Fatalf("gather with keys that share one label: %v", err)
	}
	expectGauge(t, telemetry, "devshard_gateway_rpc_stats_up", labels{"participant_key": "unknown"}, 1)

	recorder.Replace(map[string]*transport.RPCStatsSnapshot{})
	expectAbsent(t, telemetry, "devshard_gateway_rpc_stats_up")
	expectAbsent(t, telemetry, "devshard_gateway_rpc_requests_last_minute")
}
