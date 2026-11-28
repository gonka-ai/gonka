package metrics

import (
	"maps"
	"slices"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"devshard/transport"
	"devshard/transport/rpcpb/rpcpbconnect"
)

var (
	rpcEndpoints = knownLabels(
		transport.RPCEndpointName(rpcpbconnect.PeerAuthServiceAttachProcedure),
		transport.RPCEndpointName(rpcpbconnect.PeerAuthServiceWatchProcedure),
		transport.RPCEndpointName(rpcpbconnect.SessionServiceChatProcedure),
		transport.RPCEndpointName(rpcpbconnect.SessionServiceSeedHeightSyncProcedure),
		transport.RPCEndpointName(rpcpbconnect.SessionServiceRepairHeightSyncProcedure),
		transport.RPCEndpointName(rpcpbconnect.SessionServiceVerifyTimeoutProcedure),
		transport.RPCEndpointName(rpcpbconnect.SessionServiceVerifyErrorMissProcedure),
		transport.RPCEndpointName(rpcpbconnect.SessionServiceChallengeReceiptProcedure),
		transport.RPCEndpointName(rpcpbconnect.SessionServiceGetDiffsProcedure),
		transport.RPCEndpointName(rpcpbconnect.SessionServiceGetMempoolProcedure),
		transport.RPCEndpointName(rpcpbconnect.SessionServiceGetStateProcedure),
		transport.RPCEndpointName(rpcpbconnect.SessionServiceGetSignaturesProcedure),
		transport.RPCEndpointName(rpcpbconnect.GossipServiceNonceProcedure),
		transport.RPCEndpointName(rpcpbconnect.GossipServiceTxsProcedure),
		transport.RPCEndpointName(rpcpbconnect.PayloadServiceGetPayloadProcedure),
	)
	rpcZones = knownLabels(
		transport.RPCZoneShared, transport.RPCZoneStreams,
		transport.RPCZoneAttachFloor, transport.RPCZoneAttachOrigin,
	)
	rpcReconnectReasons = knownLabels(transport.ReconnectFirstAttach, hostRPCReconnectWatch, hostRPCReconnectTTL)
)

// HostRPCRecorder exports the last closed minute of each live host's rate-limiter counters. See ../hostrpc/README.md.
type HostRPCRecorder struct {
	mu        sync.RWMutex
	snapshots map[string]*transport.RPCStatsSnapshot

	requests         *prometheus.Desc
	banned           *prometheus.Desc
	endpointRequests *prometheus.Desc
	endpointBanned   *prometheus.Desc
	zoneRequests     *prometheus.Desc
	zoneBanned       *prometheus.Desc
	attachAttempts   *prometheus.Desc
	attachBanned     *prometheus.Desc
	reconnects       *prometheus.Desc
	statsUp          *prometheus.Desc
	minuteUnix       *prometheus.Desc
	partial          *prometheus.Desc
}

func NewHostRPCRecorder(telemetry *Metrics) *HostRPCRecorder {
	recorder := &HostRPCRecorder{
		requests: gaugeDesc("devshard_gateway_rpc_requests_last_minute",
			"Inbound RPCs the host served in its last closed minute, every caller's, not only this gateway's.", hostRPCParticipant),
		banned: gaugeDesc("devshard_gateway_rpc_banned_last_minute",
			"Inbound RPCs the host's rate limiter refused in its last closed minute, every caller's, not only this gateway's.", hostRPCParticipant),
		endpointRequests: gaugeDesc("devshard_gateway_rpc_endpoint_requests_last_minute",
			"Inbound RPCs in the host's last closed minute by classified endpoint. Unknown names sum into other.", hostRPCParticipant, hostRPCEndpoint),
		endpointBanned: gaugeDesc("devshard_gateway_rpc_endpoint_banned_last_minute",
			"Refused RPCs in the host's last closed minute by classified endpoint. Unknown names sum into other.", hostRPCParticipant, hostRPCEndpoint),
		zoneRequests: gaugeDesc("devshard_gateway_rpc_zone_requests_last_minute",
			"Inbound RPCs in the host's last closed minute by rate-limit zone. Unknown names sum into other.", hostRPCParticipant, hostRPCZone),
		zoneBanned: gaugeDesc("devshard_gateway_rpc_zone_banned_last_minute",
			"Refused RPCs in the host's last closed minute by rate-limit zone. Unknown names sum into other.", hostRPCParticipant, hostRPCZone),
		attachAttempts: gaugeDesc("devshard_gateway_rpc_attach_attempts_last_minute",
			"Attach attempts the host saw in its last closed minute.", hostRPCParticipant),
		attachBanned: gaugeDesc("devshard_gateway_rpc_attach_banned_last_minute",
			"Attach attempts the host's throttle refused in its last closed minute, by reason: floor, origin or other.", hostRPCParticipant, hostRPCReason),
		reconnects: gaugeDesc("devshard_gateway_rpc_reconnects_last_minute",
			"Outbound Attach and re-attach attempts the host made in its last closed minute, summed over peers. Unknown reasons sum into other.", hostRPCParticipant, hostRPCReason),
		statsUp: gaugeDesc("devshard_gateway_rpc_stats_up",
			"Whether the last poll of this host's RPC stats was answered. A host that did not answer exports only this series.", hostRPCParticipant),
		minuteUnix: gaugeDesc("devshard_gateway_rpc_minute_unix",
			"Unix timestamp of the closed minute the host's counters describe.", hostRPCParticipant),
		partial: gaugeDesc("devshard_gateway_rpc_stats_partial",
			"Whether the host's snapshot merged only some of its protocol versions.", hostRPCParticipant),
	}
	telemetry.Register(recorder)
	return recorder
}

// Replace swaps in the finished wave, so a host that left the live set stops exporting.
func (r *HostRPCRecorder) Replace(snapshots map[string]*transport.RPCStatsSnapshot) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.snapshots = snapshots
	r.mu.Unlock()
}

func (r *HostRPCRecorder) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		r.requests, r.banned, r.endpointRequests, r.endpointBanned, r.zoneRequests, r.zoneBanned,
		r.attachAttempts, r.attachBanned, r.reconnects, r.statsUp, r.minuteUnix, r.partial,
	} {
		ch <- desc
	}
}

func (r *HostRPCRecorder) Collect(ch chan<- prometheus.Metric) {
	r.mu.RLock()
	snapshots := r.snapshots
	r.mu.RUnlock()
	reported := make(map[string]struct{}, len(snapshots))
	for _, key := range slices.Sorted(maps.Keys(snapshots)) {
		snapshot := snapshots[key]
		participant := metricLabel(key, labelUnknown)
		if _, duplicate := reported[participant]; duplicate {
			continue
		}
		reported[participant] = struct{}{}
		gauge(ch, r.statsUp, boolGauge(snapshot != nil), participant)
		if snapshot != nil {
			r.collectSnapshot(ch, participant, snapshot)
		}
	}
}

func (r *HostRPCRecorder) collectSnapshot(ch chan<- prometheus.Metric, participant string, snapshot *transport.RPCStatsSnapshot) {
	host := snapshot.Host
	gauge(ch, r.requests, float64(host.Requests), participant)
	gauge(ch, r.banned, float64(host.Banned), participant)
	for endpoint, count := range foldRPCEndpoints(host.Endpoints) {
		gauge(ch, r.endpointRequests, float64(count.requests), participant, endpoint)
		gauge(ch, r.endpointBanned, float64(count.banned), participant, endpoint)
	}
	for zone, count := range foldRPCZones(host.Zones) {
		gauge(ch, r.zoneRequests, float64(count.requests), participant, zone)
		gauge(ch, r.zoneBanned, float64(count.banned), participant, zone)
	}
	gauge(ch, r.attachAttempts, float64(host.Attach.Attempts), participant)
	gauge(ch, r.attachBanned, float64(host.Attach.BannedFloor), participant, hostRPCAttachFloor)
	gauge(ch, r.attachBanned, float64(host.Attach.BannedOrigin), participant, hostRPCAttachOrigin)
	gauge(ch, r.attachBanned, float64(attachBannedOther(host.Attach)), participant, hostRPCOther)
	for reason, attempts := range foldRPCReconnects(host.Reconnects) {
		gauge(ch, r.reconnects, float64(attempts), participant, reason)
	}
	gauge(ch, r.minuteUnix, float64(snapshot.MinuteUnix), participant)
	gauge(ch, r.partial, boolGauge(snapshot.Partial), participant)
}

type rpcCount struct {
	requests uint64
	banned   uint64
}

func knownLabels(names ...string) map[string]struct{} {
	known := make(map[string]struct{}, len(names))
	for _, name := range names {
		known[name] = struct{}{}
	}
	return known
}

func rpcLabel(known map[string]struct{}, name string) string {
	if _, ok := known[name]; ok {
		return name
	}
	return hostRPCOther
}

func attachBannedOther(attach transport.RPCStatsAttach) uint64 {
	named := attach.BannedFloor + attach.BannedOrigin
	if attach.Banned <= named {
		return 0
	}
	return attach.Banned - named
}

func foldRPCEndpoints(rows []transport.RPCStatsEndpoint) map[string]rpcCount {
	folded := make(map[string]rpcCount, len(rows))
	for _, row := range rows {
		name := rpcLabel(rpcEndpoints, row.Endpoint)
		count := folded[name]
		count.requests += row.Requests
		count.banned += row.Banned
		folded[name] = count
	}
	return folded
}

func foldRPCZones(rows []transport.RPCStatsZone) map[string]rpcCount {
	folded := make(map[string]rpcCount, len(rows))
	for _, row := range rows {
		name := rpcLabel(rpcZones, row.Zone)
		count := folded[name]
		count.requests += row.Requests
		count.banned += row.Banned
		folded[name] = count
	}
	return folded
}

func foldRPCReconnects(rows []transport.RPCStatsReconnect) map[string]uint64 {
	folded := make(map[string]uint64, len(rows))
	for _, row := range rows {
		folded[rpcLabel(rpcReconnectReasons, row.Reason)] += row.Attempts
	}
	return folded
}
