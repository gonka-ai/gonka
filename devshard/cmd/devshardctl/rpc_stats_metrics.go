package main

import (
	"github.com/prometheus/client_golang/prometheus"

	"devshard/transport"
)

type rpcStatsDescs struct {
	requests         *prometheus.Desc
	banned           *prometheus.Desc
	endpointRequests *prometheus.Desc
	endpointBanned   *prometheus.Desc
	zoneRequests     *prometheus.Desc
	zoneBanned       *prometheus.Desc
	peerRequests     *prometheus.Desc
	peerBanned       *prometheus.Desc
	ipRequests       *prometheus.Desc
	ipBanned         *prometheus.Desc
	attachAttempts   *prometheus.Desc
	attachBanned     *prometheus.Desc
	reconnects       *prometheus.Desc
	statsUp          *prometheus.Desc
}

func newRPCStatsDescs() rpcStatsDescs {
	return rpcStatsDescs{
		requests: prometheus.NewDesc(
			"devshard_gateway_rpc_requests_last_minute",
			"Inbound RPCs in the last closed minute (escrow=_ is the host process total).",
			[]string{"host", "escrow"}, nil,
		),
		banned: prometheus.NewDesc(
			"devshard_gateway_rpc_banned_last_minute",
			"Rate-limited inbound RPCs in the last closed minute (escrow=_ is the host process total).",
			[]string{"host", "escrow"}, nil,
		),
		endpointRequests: prometheus.NewDesc(
			"devshard_gateway_rpc_endpoint_requests_last_minute",
			"Inbound RPCs in the last closed minute by classified endpoint.",
			[]string{"host", "escrow", "endpoint"}, nil,
		),
		endpointBanned: prometheus.NewDesc(
			"devshard_gateway_rpc_endpoint_banned_last_minute",
			"Banned RPCs in the last closed minute by classified endpoint.",
			[]string{"host", "escrow", "endpoint"}, nil,
		),
		zoneRequests: prometheus.NewDesc(
			"devshard_gateway_rpc_zone_requests_last_minute",
			"Inbound RPCs in the last closed minute by rate-limit zone.",
			[]string{"host", "zone"}, nil,
		),
		zoneBanned: prometheus.NewDesc(
			"devshard_gateway_rpc_zone_banned_last_minute",
			"Banned RPCs in the last closed minute by rate-limit zone.",
			[]string{"host", "zone"}, nil,
		),
		peerRequests: prometheus.NewDesc(
			"devshard_gateway_rpc_peer_requests_last_minute",
			"Inbound RPCs in the last closed minute by authenticated peer.",
			[]string{"host", "escrow", "peer"}, nil,
		),
		peerBanned: prometheus.NewDesc(
			"devshard_gateway_rpc_peer_banned_last_minute",
			"Banned RPCs in the last closed minute by authenticated peer.",
			[]string{"host", "escrow", "peer"}, nil,
		),
		ipRequests: prometheus.NewDesc(
			"devshard_gateway_rpc_ip_requests_last_minute",
			"Inbound RPCs in the last closed minute by trusted client IP.",
			[]string{"host", "ip"}, nil,
		),
		ipBanned: prometheus.NewDesc(
			"devshard_gateway_rpc_ip_banned_last_minute",
			"Banned RPCs in the last closed minute by trusted client IP.",
			[]string{"host", "ip"}, nil,
		),
		attachAttempts: prometheus.NewDesc(
			"devshard_gateway_rpc_attach_attempts_last_minute",
			"Attach attempts in the last closed minute.",
			[]string{"host"}, nil,
		),
		attachBanned: prometheus.NewDesc(
			"devshard_gateway_rpc_attach_banned_last_minute",
			"Attach throttle refusals in the last closed minute (reason=floor or other).",
			[]string{"host", "reason"}, nil,
		),
		reconnects: prometheus.NewDesc(
			"devshard_gateway_rpc_reconnects_last_minute",
			"Outbound Attach / re-attach attempts in the last closed minute.",
			[]string{"host", "peer", "reason"}, nil,
		),
		statsUp: prometheus.NewDesc(
			"devshard_gateway_rpc_stats_up",
			"1 if the last scrape of this host's /devshard/stats/rpc succeeded.",
			[]string{"host"}, nil,
		),
	}
}

func (d rpcStatsDescs) describe(ch chan<- *prometheus.Desc) {
	if d.requests == nil {
		return
	}
	ch <- d.requests
	ch <- d.banned
	ch <- d.endpointRequests
	ch <- d.endpointBanned
	ch <- d.zoneRequests
	ch <- d.zoneBanned
	ch <- d.peerRequests
	ch <- d.peerBanned
	ch <- d.ipRequests
	ch <- d.ipBanned
	ch <- d.attachAttempts
	ch <- d.attachBanned
	ch <- d.reconnects
	ch <- d.statsUp
}

func (d rpcStatsDescs) emit(ch chan<- prometheus.Metric, hosts []rpcStatsHostState) {
	if d.requests == nil {
		return
	}
	for _, st := range hosts {
		host := metricLabel(st.Participant, "unknown")
		up := 0.0
		if st.Up {
			up = 1
		}
		ch <- prometheus.MustNewConstMetric(d.statsUp, prometheus.GaugeValue, up, host)
		if !st.HasSnap {
			continue
		}
		snap := st.Snap
		emitReqBan(ch, d.requests, d.banned, host, rpcStatsProcessEscrow, snap.Host.Requests, snap.Host.Banned)
		for ep, c := range foldEndpointCounts(snap.Host.Endpoints) {
			ch <- prometheus.MustNewConstMetric(d.endpointRequests, prometheus.GaugeValue, float64(c.requests), host, rpcStatsProcessEscrow, ep)
			ch <- prometheus.MustNewConstMetric(d.endpointBanned, prometheus.GaugeValue, float64(c.banned), host, rpcStatsProcessEscrow, ep)
		}
		for _, z := range snap.Host.Zones {
			zone := z.Zone
			ch <- prometheus.MustNewConstMetric(d.zoneRequests, prometheus.GaugeValue, float64(z.Requests), host, zone)
			ch <- prometheus.MustNewConstMetric(d.zoneBanned, prometheus.GaugeValue, float64(z.Banned), host, zone)
		}
		for peer, c := range foldPeerCounts(snap.Host.Peers) {
			ch <- prometheus.MustNewConstMetric(d.peerRequests, prometheus.GaugeValue, float64(c.requests), host, rpcStatsProcessEscrow, peer)
			ch <- prometheus.MustNewConstMetric(d.peerBanned, prometheus.GaugeValue, float64(c.banned), host, rpcStatsProcessEscrow, peer)
		}
		for _, ip := range snap.Host.IPs {
			ipLabel := ip.IP
			ch <- prometheus.MustNewConstMetric(d.ipRequests, prometheus.GaugeValue, float64(ip.Requests), host, ipLabel)
			ch <- prometheus.MustNewConstMetric(d.ipBanned, prometheus.GaugeValue, float64(ip.Banned), host, ipLabel)
		}
		ch <- prometheus.MustNewConstMetric(d.attachAttempts, prometheus.GaugeValue, float64(snap.Host.Attach.Attempts), host)
		ch <- prometheus.MustNewConstMetric(d.attachBanned, prometheus.GaugeValue, float64(snap.Host.Attach.BannedFloor), host, rpcStatsAttachFloor)
		if other := attachOtherBanned(snap.Host.Attach); other > 0 {
			ch <- prometheus.MustNewConstMetric(d.attachBanned, prometheus.GaugeValue, float64(other), host, rpcStatsAttachOther)
		}
		for _, r := range snap.Host.Reconnects {
			ch <- prometheus.MustNewConstMetric(d.reconnects, prometheus.GaugeValue, float64(r.Attempts), host, r.Peer, r.Reason)
		}
		for escrow, sh := range foldShards(snap.Shards) {
			emitReqBan(ch, d.requests, d.banned, host, escrow, sh.requests, sh.banned)
			for ep, c := range sh.endpoints {
				ch <- prometheus.MustNewConstMetric(d.endpointRequests, prometheus.GaugeValue, float64(c.requests), host, escrow, ep)
				ch <- prometheus.MustNewConstMetric(d.endpointBanned, prometheus.GaugeValue, float64(c.banned), host, escrow, ep)
			}
			for peer, c := range sh.peers {
				ch <- prometheus.MustNewConstMetric(d.peerRequests, prometheus.GaugeValue, float64(c.requests), host, escrow, peer)
				ch <- prometheus.MustNewConstMetric(d.peerBanned, prometheus.GaugeValue, float64(c.banned), host, escrow, peer)
			}
		}
	}
}

type rpcCount struct {
	requests uint64
	banned   uint64
}

type foldedShard struct {
	requests  uint64
	banned    uint64
	endpoints map[string]rpcCount
	peers     map[string]rpcCount
}

func emitReqBan(ch chan<- prometheus.Metric, reqDesc, banDesc *prometheus.Desc, host, escrow string, requests, banned uint64) {
	ch <- prometheus.MustNewConstMetric(reqDesc, prometheus.GaugeValue, float64(requests), host, escrow)
	ch <- prometheus.MustNewConstMetric(banDesc, prometheus.GaugeValue, float64(banned), host, escrow)
}

func attachOtherBanned(a transport.RPCStatsAttach) uint64 {
	if a.Banned <= a.BannedFloor {
		return 0
	}
	return a.Banned - a.BannedFloor
}

func foldEndpointCounts(rows []transport.RPCStatsEndpoint) map[string]rpcCount {
	out := make(map[string]rpcCount, len(rows))
	for _, e := range rows {
		cur := out[e.Endpoint]
		cur.requests += e.Requests
		cur.banned += e.Banned
		out[e.Endpoint] = cur
	}
	return out
}

func foldPeerCounts(rows []transport.RPCStatsPeer) map[string]rpcCount {
	out := make(map[string]rpcCount, len(rows))
	for _, p := range rows {
		cur := out[p.Peer]
		cur.requests += p.Requests
		cur.banned += p.Banned
		out[p.Peer] = cur
	}
	return out
}

func foldShards(shards []transport.RPCStatsShard) map[string]*foldedShard {
	out := make(map[string]*foldedShard, len(shards))
	for _, sh := range shards {
		cur := out[sh.EscrowID]
		if cur == nil {
			cur = &foldedShard{
				endpoints: make(map[string]rpcCount),
				peers:     make(map[string]rpcCount),
			}
			out[sh.EscrowID] = cur
		}
		cur.requests += sh.Requests
		cur.banned += sh.Banned
		for ep, c := range foldEndpointCounts(sh.Endpoints) {
			x := cur.endpoints[ep]
			x.requests += c.requests
			x.banned += c.banned
			cur.endpoints[ep] = x
		}
		for peer, c := range foldPeerCounts(sh.Peers) {
			x := cur.peers[peer]
			x.requests += c.requests
			x.banned += c.banned
			cur.peers[peer] = x
		}
	}
	return out
}
