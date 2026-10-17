package main

import (
	"sort"

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
	attachAttempts   *prometheus.Desc
	attachBanned     *prometheus.Desc
	reconnects       *prometheus.Desc
	statsUp          *prometheus.Desc
	minuteUnix       *prometheus.Desc
	partial          *prometheus.Desc
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
			"Inbound RPCs in the last closed minute by classified endpoint. Unknown names sum into other.",
			[]string{"host", "escrow", "endpoint"}, nil,
		),
		endpointBanned: prometheus.NewDesc(
			"devshard_gateway_rpc_endpoint_banned_last_minute",
			"Banned RPCs in the last closed minute by classified endpoint. Unknown names sum into other.",
			[]string{"host", "escrow", "endpoint"}, nil,
		),
		zoneRequests: prometheus.NewDesc(
			"devshard_gateway_rpc_zone_requests_last_minute",
			"Inbound RPCs in the last closed minute by rate-limit zone. Unknown names sum into other.",
			[]string{"host", "zone"}, nil,
		),
		zoneBanned: prometheus.NewDesc(
			"devshard_gateway_rpc_zone_banned_last_minute",
			"Banned RPCs in the last closed minute by rate-limit zone. Unknown names sum into other.",
			[]string{"host", "zone"}, nil,
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
			"Outbound Attach / re-attach attempts in the last closed minute. Rows past the per-snapshot cap, and unknown reasons, sum into other.",
			[]string{"host", "peer", "reason"}, nil,
		),
		statsUp: prometheus.NewDesc(
			"devshard_gateway_rpc_stats_up",
			"1 if the last scrape of this host's /devshard/stats/rpc succeeded.",
			[]string{"host"}, nil,
		),
		minuteUnix: prometheus.NewDesc(
			"devshard_gateway_rpc_minute_unix",
			"Unix timestamp of the last closed minute in the cached snapshot. Join logs with tag=rpc_stats and rpc_stats_join=host/minute_unix.",
			[]string{"host"}, nil,
		),
		partial: prometheus.NewDesc(
			"devshard_gateway_rpc_stats_partial",
			"1 if the last snapshot was a partial versiond merge.",
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
	ch <- d.attachAttempts
	ch <- d.attachBanned
	ch <- d.reconnects
	ch <- d.statsUp
	ch <- d.minuteUnix
	ch <- d.partial
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
		for zone, c := range foldZoneCounts(snap.Host.Zones) {
			ch <- prometheus.MustNewConstMetric(d.zoneRequests, prometheus.GaugeValue, float64(c.requests), host, zone)
			ch <- prometheus.MustNewConstMetric(d.zoneBanned, prometheus.GaugeValue, float64(c.banned), host, zone)
		}
		ch <- prometheus.MustNewConstMetric(d.minuteUnix, prometheus.GaugeValue, float64(snap.MinuteUnix), host)
		partial := 0.0
		if snap.Partial {
			partial = 1
		}
		ch <- prometheus.MustNewConstMetric(d.partial, prometheus.GaugeValue, partial, host)
		ch <- prometheus.MustNewConstMetric(d.attachAttempts, prometheus.GaugeValue, float64(snap.Host.Attach.Attempts), host)
		ch <- prometheus.MustNewConstMetric(d.attachBanned, prometheus.GaugeValue, float64(snap.Host.Attach.BannedFloor), host, rpcStatsAttachFloor)
		if other := attachOtherBanned(snap.Host.Attach); other > 0 {
			ch <- prometheus.MustNewConstMetric(d.attachBanned, prometheus.GaugeValue, float64(other), host, rpcStatsAttachOther)
		}
		for _, r := range foldReconnects(snap.Host.Reconnects) {
			ch <- prometheus.MustNewConstMetric(d.reconnects, prometheus.GaugeValue, float64(r.Attempts), host, r.Peer, r.Reason)
		}
		for escrow, sh := range foldShards(snap.Shards) {
			emitReqBan(ch, d.requests, d.banned, host, escrow, sh.requests, sh.banned)
			for ep, c := range sh.endpoints {
				ch <- prometheus.MustNewConstMetric(d.endpointRequests, prometheus.GaugeValue, float64(c.requests), host, escrow, ep)
				ch <- prometheus.MustNewConstMetric(d.endpointBanned, prometheus.GaugeValue, float64(c.banned), host, escrow, ep)
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
		name := transport.RPCStatsEndpointLabel(e.Endpoint)
		cur := out[name]
		cur.requests += e.Requests
		cur.banned += e.Banned
		out[name] = cur
	}
	return out
}

func foldZoneCounts(rows []transport.RPCStatsZone) map[string]rpcCount {
	out := make(map[string]rpcCount, len(rows))
	for _, z := range rows {
		name := transport.RPCStatsZoneLabel(z.Zone)
		cur := out[name]
		cur.requests += z.Requests
		cur.banned += z.Banned
		out[name] = cur
	}
	return out
}

func foldReconnects(rows []transport.RPCStatsReconnect) []transport.RPCStatsReconnect {
	merged := make(map[[2]string]uint64, len(rows))
	for _, r := range rows {
		peer := r.Peer
		if peer == "" {
			peer = transport.RPCStatsOtherLabel
		}
		reason := transport.RPCStatsReconnectReason(r.Reason)
		merged[[2]string{peer, reason}] += r.Attempts
	}
	return capPairs(merged, rpcStatsMaxReconnectRows, transport.RPCStatsOtherLabel, transport.RPCStatsOtherLabel)
}

func foldShards(shards []transport.RPCStatsShard) map[string]*foldedShard {
	out := make(map[string]*foldedShard, len(shards))
	for _, sh := range shards {
		id := shardLabel(sh.EscrowID)
		cur := out[id]
		if cur == nil {
			cur = &foldedShard{endpoints: make(map[string]rpcCount)}
			out[id] = cur
		}
		cur.requests += sh.Requests
		cur.banned += sh.Banned
		for ep, c := range foldEndpointCounts(sh.Endpoints) {
			x := cur.endpoints[ep]
			x.requests += c.requests
			x.banned += c.banned
			cur.endpoints[ep] = x
		}
	}
	if len(out) <= rpcStatsMaxShardRows {
		return out
	}
	type row struct {
		id string
		sh *foldedShard
	}
	list := make([]row, 0, len(out))
	var overflow *foldedShard
	for id, sh := range out {
		if id == transport.RPCStatsOtherLabel {
			overflow = sh
			continue
		}
		list = append(list, row{id: id, sh: sh})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].sh.requests != list[j].sh.requests {
			return list[i].sh.requests > list[j].sh.requests
		}
		return list[i].id < list[j].id
	})
	keep := rpcStatsMaxShardRows - 1
	if keep < 0 {
		keep = 0
	}
	if len(list) > keep {
		if overflow == nil {
			overflow = &foldedShard{endpoints: make(map[string]rpcCount)}
		}
		for _, extra := range list[keep:] {
			mergeShard(overflow, extra.sh)
		}
		list = list[:keep]
	}
	kept := make(map[string]*foldedShard, len(list)+1)
	for _, r := range list {
		kept[r.id] = r.sh
	}
	if overflow != nil {
		kept[transport.RPCStatsOtherLabel] = overflow
	}
	return kept
}

// shardLabel is the escrow label for one snapshot row. "_" is the process
// total, emitted separately, so a shard that reuses it would duplicate the
// series and fail the scrape.
func shardLabel(id string) string {
	if id == "" || id == rpcStatsProcessEscrow {
		return transport.RPCStatsOtherLabel
	}
	return id
}

func mergeShard(dst, src *foldedShard) {
	if dst == nil || src == nil {
		return
	}
	dst.requests += src.requests
	dst.banned += src.banned
	if dst.endpoints == nil {
		dst.endpoints = make(map[string]rpcCount, len(src.endpoints))
	}
	for ep, c := range src.endpoints {
		x := dst.endpoints[ep]
		x.requests += c.requests
		x.banned += c.banned
		dst.endpoints[ep] = x
	}
}

func capPairs(merged map[[2]string]uint64, cap int, overflowA, overflowB string) []transport.RPCStatsReconnect {
	if len(merged) == 0 {
		return nil
	}
	if len(merged) <= cap {
		return reconnectSlice(merged)
	}
	overflowKey := [2]string{overflowA, overflowB}
	overflow := merged[overflowKey]
	delete(merged, overflowKey)
	list := reconnectSlice(merged)
	sort.Slice(list, func(i, j int) bool {
		if list[i].Attempts != list[j].Attempts {
			return list[i].Attempts > list[j].Attempts
		}
		if list[i].Peer != list[j].Peer {
			return list[i].Peer < list[j].Peer
		}
		return list[i].Reason < list[j].Reason
	})
	keep := cap - 1
	if keep < 0 {
		keep = 0
	}
	if len(list) > keep {
		for _, extra := range list[keep:] {
			overflow += extra.Attempts
		}
		list = list[:keep]
	}
	if overflow > 0 {
		list = append(list, transport.RPCStatsReconnect{
			Peer: overflowA, Reason: overflowB, Attempts: overflow,
		})
	}
	return list
}

func reconnectSlice(merged map[[2]string]uint64) []transport.RPCStatsReconnect {
	out := make([]transport.RPCStatsReconnect, 0, len(merged))
	for k, n := range merged {
		out = append(out, transport.RPCStatsReconnect{Peer: k[0], Reason: k[1], Attempts: n})
	}
	return out
}
