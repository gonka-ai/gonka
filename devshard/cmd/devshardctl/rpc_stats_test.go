package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"devshard/transport"
)

func TestRPCStatsPollerIdlePool(t *testing.T) {
	p := newRPCStatsPoller(nil, rpcStatsConfig{Timeout: 2 * time.Second, Concurrency: 8, Interval: 15 * time.Second})
	tr, ok := p.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.Equal(t, 512, tr.MaxIdleConns)
	require.Equal(t, 1, tr.MaxIdleConnsPerHost)
	require.Equal(t, 45*time.Second, tr.IdleConnTimeout)
}

func testRPCSnap(peer string, req, banned uint64) transport.RPCStatsSnapshot {
	attach := transport.RPCStatsAttach{Attempts: 4}
	if banned > 0 {
		attach.Banned = 1
		attach.BannedFloor = 1
	}
	return transport.RPCStatsSnapshot{
		HostAddress: "gonka1host",
		MinuteUnix:  1_710_000_000,
		Host: transport.RPCStatsHost{
			Requests: req,
			Banned:   banned,
			Endpoints: []transport.RPCStatsEndpoint{
				{Endpoint: "GetDiffs", Zone: "shared", Requests: req, Banned: banned},
			},
			Zones:  []transport.RPCStatsZone{{Zone: "shared", Requests: req, Banned: banned}},
			Peers:  []transport.RPCStatsPeer{{Peer: peer, Requests: req, Banned: banned}},
			IPs:    []transport.RPCStatsIP{{IP: "203.0.113.1", Requests: req, Banned: banned}},
			Attach: attach,
		},
		Shards: []transport.RPCStatsShard{{
			EscrowID: "42",
			Requests: req,
			Banned:   banned,
			Endpoints: []transport.RPCStatsEndpoint{
				{Endpoint: "GetDiffs", Zone: "shared", Requests: req, Banned: banned},
			},
			Peers: []transport.RPCStatsPeer{{Peer: peer, Requests: req, Banned: banned}},
		}},
	}
}

func gatherRPCStats(t *testing.T, g *Gateway) []*dto.MetricFamily {
	t.Helper()
	collector := newGatewayMetricsCollectorWithHostConnections(g, fakeHostConnectionSnapshotter(nil))
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	require.NoError(t, err)
	return families
}

func TestGatewayRPCStatsCollectorAggregatesNoPeerSeries(t *testing.T) {
	p := &rpcStatsPoller{byHost: map[string]*rpcStatsHostState{
		"gonka1a": {
			Participant: "gonka1a",
			Up:          true,
			HasSnap:     true,
			Snap:        testRPCSnap("gonka1peer", 10, 2),
		},
		"gonka1b": {
			Participant: "gonka1b",
			Up:          true,
			HasSnap:     true,
			Snap:        testRPCSnap("gonka1other", 5, 0),
		},
	}}
	g := &Gateway{rpcStats: p}
	families := gatherRPCStats(t, g)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_requests_last_minute", map[string]string{"host": "gonka1a", "escrow": "_"}, 10)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_banned_last_minute", map[string]string{"host": "gonka1a", "escrow": "_"}, 2)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_requests_last_minute", map[string]string{"host": "gonka1a", "escrow": "42"}, 10)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_endpoint_requests_last_minute", map[string]string{"host": "gonka1a", "escrow": "_", "endpoint": "GetDiffs"}, 10)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_endpoint_banned_last_minute", map[string]string{"host": "gonka1a", "escrow": "_", "endpoint": "GetDiffs"}, 2)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_zone_banned_last_minute", map[string]string{"host": "gonka1a", "zone": "shared"}, 2)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_attach_attempts_last_minute", map[string]string{"host": "gonka1a"}, 4)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_attach_banned_last_minute", map[string]string{"host": "gonka1a", "reason": "floor"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_stats_up", map[string]string{"host": "gonka1a"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_stats_up", map[string]string{"host": "gonka1b"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_minute_unix", map[string]string{"host": "gonka1a"}, 1_710_000_000)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_stats_partial", map[string]string{"host": "gonka1a"}, 0)
	partial := testRPCSnap("gonka1peer", 10, 2)
	partial.Partial = true
	p.byHost["gonka1a"] = &rpcStatsHostState{
		Participant: "gonka1a",
		Up:          true,
		HasSnap:     true,
		Snap:        partial,
	}
	families = gatherRPCStats(t, g)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_stats_partial", map[string]string{"host": "gonka1a"}, 1)
	requireNoMetricFamily(t, families, "devshard_gateway_rpc_peer_requests_last_minute")

	p.byHost["gonka1a"] = &rpcStatsHostState{
		Participant: "gonka1a",
		Up:          true,
		HasSnap:     true,
		Snap:        testRPCSnap("gonka1kept", 3, 0),
	}
	families = gatherRPCStats(t, g)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_requests_last_minute", map[string]string{"host": "gonka1a", "escrow": "_"}, 3)
	requireNoMetricFamily(t, families, "devshard_gateway_rpc_peer_requests_last_minute")
}

func TestGatewayRPCStatsUpZeroKeepsLastGood(t *testing.T) {
	p := &rpcStatsPoller{byHost: map[string]*rpcStatsHostState{
		"gonka1a": {
			Participant: "gonka1a",
			Up:          false,
			HasSnap:     true,
			Snap:        testRPCSnap("gonka1peer", 10, 2),
		},
	}}
	families := gatherRPCStats(t, &Gateway{rpcStats: p})
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_stats_up", map[string]string{"host": "gonka1a"}, 0)
	requireMetricGaugeValue(t, families, "devshard_gateway_rpc_requests_last_minute", map[string]string{"host": "gonka1a", "escrow": "_"}, 10)
}

func TestGatewayRPCStatsWarnDebounce(t *testing.T) {
	var n atomic.Int32
	var lastMinute atomic.Int64
	p := &rpcStatsPoller{warn: func(minute int64, hosts []rpcStatsHostState) {
		n.Add(1)
		lastMinute.Store(minute)
		require.Len(t, hosts, 1)
		require.Equal(t, uint64(2), hosts[0].Snap.Host.Banned)
		require.Equal(t, "gonka1host", hosts[0].Snap.HostAddress)
		require.Equal(t, "gonka1peer", hosts[0].Snap.Host.Peers[0].Peer)
		require.Equal(t, uint64(2), hosts[0].Snap.Host.Peers[0].Banned)
	}}
	banned := []rpcStatsHostState{{
		Participant: "gonka1outsider",
		Up:          true,
		HasSnap:     true,
		Snap:        testRPCSnap("gonka1peer", 10, 2),
	}}
	for i := 0; i < 4; i++ {
		p.maybeWarn(banned)
	}
	require.Equal(t, int32(1), n.Load())
	require.Equal(t, int64(1_710_000_000), lastMinute.Load())

	next := testRPCSnap("gonka1peer", 10, 2)
	next.MinuteUnix = 1_710_000_060
	p.maybeWarn([]rpcStatsHostState{{Participant: "gonka1outsider", Up: true, HasSnap: true, Snap: next}})
	require.Equal(t, int32(2), n.Load())
	require.Equal(t, int64(1_710_000_060), lastMinute.Load())

	clean := testRPCSnap("gonka1peer", 10, 0)
	clean.Host.Attach = transport.RPCStatsAttach{}
	clean.MinuteUnix = 1_710_000_120
	p.maybeWarn([]rpcStatsHostState{{Participant: "gonka1outsider", Up: true, HasSnap: true, Snap: clean}})
	require.Equal(t, int32(2), n.Load())
}

func TestGatewayRPCStatsWarnOneLinePerBannedHost(t *testing.T) {
	var n atomic.Int32
	p := &rpcStatsPoller{warn: func(_ int64, hosts []rpcStatsHostState) {
		n.Add(1)
		require.Len(t, hosts, 2)
		require.Equal(t, "gonka1a", hosts[0].Participant)
		require.Equal(t, "gonka1b", hosts[1].Participant)
	}}
	a := testRPCSnap("gonka1peer", 10, 2)
	b := testRPCSnap("gonka1other", 4, 1)
	b.HostAddress = "gonka1host-b"
	p.maybeWarn([]rpcStatsHostState{
		{Participant: "gonka1b", Up: true, HasSnap: true, Snap: b},
		{Participant: "gonka1a", Up: true, HasSnap: true, Snap: a},
	})
	require.Equal(t, int32(1), n.Load())
	p.maybeWarn([]rpcStatsHostState{
		{Participant: "gonka1a", Up: true, HasSnap: true, Snap: a},
	})
	require.Equal(t, int32(1), n.Load())
}

func TestGatewayRPCStatsPollerGzipAndUniqueDials(t *testing.T) {
	var hits atomic.Int32
	payload, err := json.Marshal(testRPCSnap("gonka1peer", 7, 0))
	require.NoError(t, err)
	gz, err := transport.GzipBestSpeed(payload)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		require.Equal(t, "/devshard/stats/rpc", r.URL.Path)
		require.Equal(t, "gzip", r.Header.Get("Accept-Encoding"))
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(gz)
	}))
	t.Cleanup(srv.Close)

	p := newRPCStatsPoller(nil, rpcStatsConfig{Timeout: 2 * time.Second, Concurrency: 8, Interval: time.Minute})
	p.targets = func() map[string]string {
		return map[string]string{
			"gonka1b": srv.URL,
			"gonka1a": srv.URL,
		}
	}
	p.poll(context.Background())
	require.Equal(t, int32(1), hits.Load())
	hosts := p.hosts()
	require.Len(t, hosts, 1)
	require.Equal(t, "gonka1a", hosts[0].Participant)
	require.True(t, hosts[0].Up)
	require.Equal(t, uint64(7), hosts[0].Snap.Host.Requests)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(down.Close)
	p.targets = func() map[string]string { return map[string]string{"gonka1a": down.URL} }
	p.poll(context.Background())
	hosts = p.hosts()
	require.Len(t, hosts, 1)
	require.False(t, hosts[0].Up)
	require.Equal(t, uint64(7), hosts[0].Snap.Host.Requests)
}

func TestLoadRPCStatsConfigZeroIntervalDisables(t *testing.T) {
	t.Setenv(rpcStatsEnvInterval, "0s")
	cfg := loadRPCStatsConfig()
	require.True(t, cfg.Disabled)

	t.Setenv(rpcStatsEnvInterval, "30s")
	t.Setenv(rpcStatsEnvDisabled, "")
	cfg = loadRPCStatsConfig()
	require.False(t, cfg.Disabled)
	require.Equal(t, 30*time.Second, cfg.Interval)
}

func TestLoadRPCStatsConfigDefaultTimeout(t *testing.T) {
	t.Setenv(rpcStatsEnvTimeout, "")
	cfg := loadRPCStatsConfig()
	require.Equal(t, 4*time.Second, cfg.Timeout)

	t.Setenv(rpcStatsEnvTimeout, "6s")
	cfg = loadRPCStatsConfig()
	require.Equal(t, 6*time.Second, cfg.Timeout)
}

func TestRPCStatsPollerDisabledDoesNotPoll(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)
	p := newRPCStatsPoller(nil, rpcStatsConfig{Disabled: true, Timeout: time.Second, Concurrency: 8})
	p.targets = func() map[string]string { return map[string]string{"gonka1a": srv.URL} }
	p.start()
	t.Cleanup(p.stop)
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(0), hits.Load())
}

func TestHandleDebugRPCTraffic(t *testing.T) {
	p := &rpcStatsPoller{byHost: map[string]*rpcStatsHostState{
		"gonka1a": {Participant: "gonka1a", Dial: "http://h", Up: true, HasSnap: true, Snap: testRPCSnap("p", 1, 0)},
	}}
	g := &Gateway{rpcStats: p}
	req := httptest.NewRequest(http.MethodGet, "/v1/debug/rpc-traffic", nil)
	rec := httptest.NewRecorder()
	g.handleDebugRPCTraffic(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	hosts, ok := body["hosts"].([]any)
	require.True(t, ok)
	require.Len(t, hosts, 1)
}

func TestUniqueInferenceDialsStableOwner(t *testing.T) {
	got := uniqueInferenceDials(map[string]string{
		"gonka1b": "http://shared:1",
		"gonka1a": "http://shared:1",
		"gonka1c": "http://other:1",
		"":        "http://x",
		"gonka1d": "",
	})
	require.Equal(t, []rpcStatsTarget{
		{Participant: "gonka1a", Dial: "http://shared:1"},
		{Participant: "gonka1c", Dial: "http://other:1"},
	}, got)
}

func TestRPCStatsURL(t *testing.T) {
	require.Equal(t, "http://h:8080/devshard/stats/rpc", rpcStatsURL("http://h:8080/"))
}

func TestChainPhaseGateInferenceURLs(t *testing.T) {
	g := NewChainPhaseGate("", time.Second)
	require.Empty(t, g.InferenceURLs())
	g.storeInferenceURLs(map[string]string{"gonka1a": "http://a:1"})
	require.Equal(t, "http://a:1", g.InferenceURLs()["gonka1a"])
}

func TestGunzipLimitedRoundTrip(t *testing.T) {
	payload := []byte(`{"minute_unix":1,"host":{"requests":1}}`)
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write(payload)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	out, err := gunzipLimited(buf.Bytes(), rpcStatsMaxBodyBytes)
	require.NoError(t, err)
	require.Equal(t, payload, out)
}
