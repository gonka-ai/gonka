package mockdapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"common/nodemanager/gen"
	commrc "common/runtimeconfig"
	"devshard/chainoracle/params"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func newBoundNodeManager(t *testing.T) *nodeManagerServer {
	t.Helper()
	nodes := []MLNode{
		{ID: "ml-a0", Endpoint: "http://ml-a0:8088"},
		{ID: "ml-a1", Endpoint: "http://ml-a1:8088"},
		{ID: "ml-b0", Endpoint: "http://ml-b0:8088"},
		{ID: "ml-shared", Endpoint: "http://ml-shared:8088"},
	}
	src, err := params.NewCachedSource(context.Background(), params.StaticFetcher{}, commrc.Snapshot{})
	require.NoError(t, err)
	paramsSrv, err := params.NewServer(params.Config{Source: src, MLNodes: nodes})
	require.NoError(t, err)
	srv := newNodeManagerServer(paramsSrv, nil, nil, false, nodes)
	srv.hosts = newHostPools(nodes,
		map[string]string{"ml-a0": "host-a", "ml-a1": "host-a", "ml-b0": "host-b"},
		map[string]string{"10.0.0.11": "host-a", "10.0.0.12": "host-b", "10.0.0.13": "host-c"})
	return srv
}

func fromPeer(ip string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000}})
}

func acquireNode(t *testing.T, srv *nodeManagerServer, ip string) string {
	t.Helper()
	resp, err := srv.AcquireMLNode(fromPeer(ip), &gen.AcquireMLNodeRequest{Model: "test-model"})
	require.NoError(t, err)
	return resp.GetNodeId()
}

// Test flow:
// 1. Bind two nodes to host-a, one to host-b, leave one unbound, and map three peer IPs.
// 2. Acquire repeatedly from host-a, host-b, host-c (no nodes) and an unmapped IP.
// 3. Require host-a and host-b to get disjoint nodes from their own pools.
// 4. Require host-c and the unmapped caller to fall back to the unbound node.
// 5. Require an excluded host-a node to leave only host-a's other node.
func TestAcquireMLNode_HostBoundPools(t *testing.T) {
	srv := newBoundNodeManager(t)

	gotA := map[string]bool{}
	gotB := map[string]bool{}
	for i := 0; i < 8; i++ {
		gotA[acquireNode(t, srv, "10.0.0.11")] = true
		gotB[acquireNode(t, srv, "10.0.0.12")] = true
		require.Equal(t, "ml-shared", acquireNode(t, srv, "10.0.0.13"))
		require.Equal(t, "ml-shared", acquireNode(t, srv, "10.9.9.9"))
	}
	require.Equal(t, map[string]bool{"ml-a0": true, "ml-a1": true}, gotA)
	require.Equal(t, map[string]bool{"ml-b0": true}, gotB)

	resp, err := srv.AcquireMLNode(fromPeer("10.0.0.11"), &gen.AcquireMLNodeRequest{Model: "test-model", ExcludedNodes: []string{"ml-a0"}})
	require.NoError(t, err)
	require.Equal(t, "ml-a1", resp.GetNodeId())

	stats := srv.hosts.snapshot()
	require.Equal(t, uint64(9), stats["host-a"].Allocations)
	require.Equal(t, uint64(8), stats["host-b"].Allocations)
	require.Equal(t, map[string]uint64{"ml-b0": 8}, stats["host-b"].NodeAllocations)
	require.Equal(t, []string{"ml-a0", "ml-a1"}, stats["host-a"].Nodes)
	require.Equal(t, uint64(8), stats["host-c"].Allocations)
	require.Equal(t, uint64(8), stats[unmappedHost].Allocations)
}

// Test flow:
// 1. Mount the testenv routes and POST /testenv/challenge {host-b, on}.
// 2. Require Acquire from host-b to fail with ResourceExhausted while host-a still succeeds.
// 3. Require GET /testenv/host-stats to show host-b challenged with one rejection.
// 4. POST /testenv/challenge {host-b, off} and require host-b to acquire its node again.
func TestAcquireMLNode_ChallengedHostExhausted(t *testing.T) {
	srv := newBoundNodeManager(t)
	e := echo.New()
	mountHostPools(e.Group(""), srv.hosts)
	post := func(body string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/testenv/challenge", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	post(`{"host":"host-b","on":true}`)
	_, err := srv.AcquireMLNode(fromPeer("10.0.0.12"), &gen.AcquireMLNodeRequest{Model: "test-model"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Contains(t, acquireNode(t, srv, "10.0.0.11"), "ml-a")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/testenv/host-stats", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var stats map[string]HostStats
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &stats))
	require.True(t, stats["host-b"].Challenged)
	require.Equal(t, uint64(1), stats["host-b"].ChallengeRejections)
	require.Zero(t, stats["host-b"].Allocations)
	require.False(t, stats["host-a"].Challenged)
	require.Equal(t, uint64(1), stats["host-a"].Allocations)

	post(`{"host":"host-b","on":false}`)
	require.Equal(t, "ml-b0", acquireNode(t, srv, "10.0.0.12"))
}

// Test flow:
// 1. Parse a valid pair list with spaces and an empty entry.
// 2. Require malformed and duplicate entries to be rejected.
func TestParsePairsEnv(t *testing.T) {
	got, err := ParsePairsEnv(" ml-0=versiond-3 ,,ml-1=versiond-4")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"ml-0": "versiond-3", "ml-1": "versiond-4"}, got)
	_, err = ParsePairsEnv("ml-0")
	require.Error(t, err)
	_, err = ParsePairsEnv("ml-0=a,ml-0=b")
	require.Error(t, err)
}

func newNamedNodeManager(t *testing.T, dns map[string][]string) *nodeManagerServer {
	t.Helper()
	nodes := []MLNode{
		{ID: "ml-a0", Endpoint: "http://ml-a0:8088"},
		{ID: "ml-a1", Endpoint: "http://ml-a1:8088"},
		{ID: "ml-b0", Endpoint: "http://ml-b0:8088"},
		{ID: "ml-b1", Endpoint: "http://ml-b1:8088"},
	}
	src, err := params.NewCachedSource(context.Background(), params.StaticFetcher{}, commrc.Snapshot{})
	require.NoError(t, err)
	paramsSrv, err := params.NewServer(params.Config{Source: src, MLNodes: nodes})
	require.NoError(t, err)
	srv := newNodeManagerServer(paramsSrv, nil, nil, false, nodes)
	srv.hosts = newHostPools(nodes,
		map[string]string{"ml-a0": "versiond-a", "ml-a1": "versiond-a", "ml-b0": "versiond-b", "ml-b1": "versiond-b"},
		map[string]string{"versiond-a": "versiond-a", "versiond-b": "versiond-b"})
	srv.hosts.minRefresh = 0
	srv.hosts.lookup = func(_ context.Context, name string) ([]string, error) {
		if ips, ok := dns[name]; ok {
			return ips, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return srv
}

// Test flow:
// 1. Map two hosts by service name and resolve them to two fake peer IPs.
// 2. Acquire repeatedly from both peers and require disjoint pools, each the peer's own nodes.
// 3. Move versiond-b to a new IP, as a restarted container would, and require the new IP to map to versiond-b.
// 4. Require host-stats to show zero unmapped allocations.
func TestAcquireMLNode_HostsMappedByName(t *testing.T) {
	dns := map[string][]string{"versiond-a": {"10.1.0.5"}, "versiond-b": {"10.1.0.6"}}
	srv := newNamedNodeManager(t, dns)

	gotA, gotB := map[string]bool{}, map[string]bool{}
	for i := 0; i < 6; i++ {
		gotA[acquireNode(t, srv, "10.1.0.5")] = true
		gotB[acquireNode(t, srv, "10.1.0.6")] = true
	}
	require.Equal(t, map[string]bool{"ml-a0": true, "ml-a1": true}, gotA)
	require.Equal(t, map[string]bool{"ml-b0": true, "ml-b1": true}, gotB)

	dns["versiond-b"] = []string{"10.1.0.9"}
	require.Contains(t, acquireNode(t, srv, "10.1.0.9"), "ml-b")

	stats := srv.hosts.snapshot()
	require.Equal(t, uint64(6), stats["versiond-a"].Allocations)
	require.Equal(t, uint64(7), stats["versiond-b"].Allocations)
	require.Zero(t, stats[unmappedHost].Allocations)
}

// Test flow:
// 1. Map two hosts by service name and challenge versiond-b.
// 2. Require the versiond-b peer to get ResourceExhausted and the versiond-a peer to acquire its own node.
// 3. Require host-stats to count the rejection on versiond-b only.
func TestAcquireMLNode_ChallengedHostByName(t *testing.T) {
	srv := newNamedNodeManager(t, map[string][]string{"versiond-a": {"10.1.0.5"}, "versiond-b": {"10.1.0.6"}})
	srv.hosts.setChallenged("versiond-b", true)

	_, err := srv.AcquireMLNode(fromPeer("10.1.0.6"), &gen.AcquireMLNodeRequest{Model: "test-model"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Contains(t, acquireNode(t, srv, "10.1.0.5"), "ml-a")

	stats := srv.hosts.snapshot()
	require.Equal(t, uint64(1), stats["versiond-b"].ChallengeRejections)
	require.Zero(t, stats["versiond-a"].ChallengeRejections)
	require.Equal(t, uint64(1), stats["versiond-a"].Allocations)
	require.Zero(t, stats[unmappedHost].Allocations)
}
