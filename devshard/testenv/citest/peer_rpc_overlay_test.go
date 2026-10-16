//go:build testenvci

package citest

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// TestPeerRPCOverlayHop is §8.3. The proxy overlay is on and is the pooled
// InferenceUrl. Peer RPC is HTTP/2 through proxy:8080, the same port that
// carries JSON, and one connection per peer on every hop. When that port
// stops speaking HTTP/2 (an older wrapping), the same peers continue over
// Connect on HTTP/1.1 through the same port.
func TestPeerRPCOverlayHop(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	requireOverlayRPC(t)
	if os.Getenv(harness.EnvVersiondImage) != "" || os.Getenv(harness.EnvVersiondRouterImage) != "" {
		t.Fatal("overlay hop is current versiond and versiond-router; unset TESTENV_VERSIOND_IMAGE and TESTENV_VERSIOND_ROUTER_IMAGE")
	}
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootProxyOverlayStack(t, "citest-peerrpc-overlay-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "proxy", "versiond-0", "versiond-1", "versiond-router", "mock-openai")
		}
	})
	version := cfg.Versiond.VersionName
	if version == "" {
		version = "v2"
	}

	proxyUp, err := stack.ServiceRunning("proxy")
	require.NoError(t, err)
	require.True(t, proxyUp, "overlay must start proxy")
	nginxUp, err := stack.ServiceRunning("nginx")
	require.NoError(t, err)
	require.False(t, nginxUp, "testenv has no nginx")

	proxyAddr := stack.ProxyH2HTTP(t)
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(proxyAddr, "http://"), 5*time.Second)
	require.NoError(t, err, "proxy must publish %s", proxyAddr)
	_ = conn.Close()

	// The overlay proxy is the InferenceUrl. There is no separate port and
	// no dial override: the gateway and the hosts find HTTP/2 in band.
	require.NotEmpty(t, cfg.Participants)
	for _, p := range cfg.Participants {
		require.Equal(t, config.ProxyOverlayURL, p.InferenceURL, "participant %s", p.Address)
	}
	upgrade, _ := stack.ComposeExecOutput("devshardctl", "printenv", "DEVSHARD_RPC_H2_UPGRADE")
	require.NotEqual(t, "false", strings.TrimSpace(upgrade))

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	postGatewayChats(t, client, eps, cfg, "citest overlay hop")
	host, _ := waitHostMetric(t, stack, cfg, 30*time.Second, "devshard_peer_rpc_requests_total", `endpoint="Chat"`, `result="ok"`)
	require.NotEmpty(t, host, "no child counted Chat ok")
	requireRouterSawPeerRPC(t, stack, "HTTP/2.0")

	proxyIP := procIPv4(overlayProxyIP(cfg))
	// The gateway keeps one mux to the proxy for peer RPC. Short JSON
	// probes to the same port do not survive the settle window.
	gatewayConns := settledPersistentTCP(t, stack, "devshardctl", proxyIP, config.DefaultProxyPort)
	require.GreaterOrEqual(t, gatewayConns, 1, "gateway holds no persistent connection to the proxy")
	require.LessOrEqual(t, gatewayConns, 2, "gateway opened more than a peer mux and one JSON keep-alive to the proxy")
	// The proxy muxes that peer onto one HTTP/2 connection to the router.
	// A host peer dial through the same proxy can hold a second one.
	proxyBackend := settledPersistentTCP(t, stack, "proxy", "", 8080)
	require.GreaterOrEqual(t, proxyBackend, 1)
	require.LessOrEqual(t, proxyBackend, 3, "proxy opened more than a gateway mux and one other peer dial to versiond-router:8080")
	escrowID := config.PrimaryEscrowID(cfg)
	upstream := harness.RequireResponseHeader(t, client, harness.RouterSessionURL(eps.RouterHTTP, version, escrowID, "/healthz"), harness.StickyUpstreamHeader)
	hostID := harness.HostIDForUpstream(cfg, upstream)
	require.NotEmpty(t, hostID, "upstream %q", upstream)
	hostIP := hostIPByID(t, cfg, hostID)
	// The version pool and the host-level pool each keep a mux and a
	// check connection on this listen. Concurrent RPCs must ride those,
	// not open another.
	routerConns := settledPersistentTCP(t, stack, "versiond-router", procIPv4(hostIP), 8080)
	require.GreaterOrEqual(t, routerConns, 1)
	require.LessOrEqual(t, routerConns, 4, "versiond-router opened more than a mux and a check per pool to %s", hostID)
	requireChildMux(t, stack, hostID)

	launchParallelChats(t, client, eps, cfg, 4)
	require.Equal(t, gatewayConns, settledPersistentTCP(t, stack, "devshardctl", proxyIP, config.DefaultProxyPort),
		"parallel RPCs opened new gateway connections to the proxy")
	require.Equal(t, proxyBackend, settledPersistentTCP(t, stack, "proxy", "", 8080),
		"parallel RPCs opened new proxy connections to versiond-router:8080")
	require.Equal(t, routerConns, settledPersistentTCP(t, stack, "versiond-router", procIPv4(hostIP), 8080),
		"parallel RPCs opened new versiond-router connections to %s", hostID)
	requireChildMux(t, stack, hostID)

	metrics := gatewayMetrics(t, client, eps.GatewayHTTP)
	require.NotContains(t, metrics, "devshard_peer_pool_exhausted_total",
		"HTTP/1.1 pool must not overflow on the h2 mux")

	requireChildUnpublished(t, stack, hostID)
	requireHTTP11PeerRPCServed(t, client, eps.RouterHTTP, version)

	// The public port stops speaking HTTP/2: the proxy resets the preface
	// instead of handing it to rpc_h2. A fresh gateway finds that out on
	// its first Attach and continues over Connect on HTTP/1.1 through the
	// same port; chat still completes and the router sees HTTP/1.1 Attach.
	harness.RejectProxyH2(t, stack)
	stack.RecreateServices(t, "proxy")
	stack.RecreateServices(t, "devshardctl")
	eps = stack.Endpoints(t, cfg)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	before := chatOKCount(t, stack, cfg)
	postGatewayChats(t, client, eps, cfg, "citest overlay http11 fallback")
	require.Greater(t, chatOKCount(t, stack, cfg), before, "chat over the HTTP/1.1 fallback must be counted on the child")
	requireRouterSawPeerRPC(t, stack, "HTTP/1.1")
	require.False(t, tcpAccepted(strings.TrimPrefix(stack.ProxyH2HTTP(t), "http://")),
		"the rewritten proxy must reset the HTTP/2 preface")
}

func requireOverlayRPC(t *testing.T) {
	t.Helper()
	if !harness.ProxyOverlayFromEnv() {
		t.Fatal("set TESTENV_PROXY_OVERLAY=1 (make citest-peerrpc-overlay)")
	}
	if h2UpgradeOff() {
		t.Fatal("leave DEVSHARD_RPC_H2_UPGRADE unset: the overlay hop is HTTP/2")
	}
}

// overlayProxyIP is the overlay proxy's pinned address (docker-compose.proxy.yml).
func overlayProxyIP(cfg *config.File) string {
	base := "172.30.0"
	if cfg != nil && cfg.Network.BaseIP != "" {
		base = cfg.Network.BaseIP
	}
	return base + ".70"
}

// requireRouterSawPeerRPC reads versiond-router's request log for an Attach
// on the given HTTP version. The router logs the request line, so this is
// the wire protocol between the proxy and the router.
func requireRouterSawPeerRPC(t *testing.T, stack *harness.Stack, proto string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		logs, err := stack.ComposeLogsTail(2000, "versiond-router")
		require.NoError(t, err)
		if strings.Contains(logs, "PeerAuthService/Attach "+proto) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("versiond-router logged no Attach on %s", proto)
		}
		time.Sleep(time.Second)
	}
}

func hostIPByID(t *testing.T, cfg *config.File, id string) string {
	t.Helper()
	for _, h := range cfg.Hosts {
		if h.ID == id {
			require.NotEmpty(t, h.IP)
			return h.IP
		}
	}
	t.Fatalf("no host %s", id)
	return ""
}

func launchParallelChats(t *testing.T, client *http.Client, eps harness.Endpoints, cfg *config.File, n int) {
	t.Helper()
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := harness.TryPostGatewayChatCompletion(client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
				Model:     config.PrimaryModelID(cfg),
				Messages:  []harness.ChatMessage{{Role: "user", Content: fmt.Sprintf("citest overlay parallel %d", i)}},
				MaxTokens: 32,
			})
			errCh <- err
		}(i)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("parallel overlay chats did not finish")
	}
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}

func gatewayMetrics(t *testing.T, client *http.Client, gateway string) string {
	t.Helper()
	resp, err := client.Get(gateway + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	return string(body)
}

// requireHTTP11PeerRPCServed sends a Connect Attach over HTTP/1.1 to the
// router's :8080. The HTTP/1.1 fallback rides this path, so the router must
// route it to the peer-RPC twin and the child must answer it (a Connect
// error for an empty request), not a router 404.
func requireHTTP11PeerRPCServed(t *testing.T, client *http.Client, router, version string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		router+"/"+version+"/sessions/1/rpc/devshard.transport.v1.PeerAuthService/Attach",
		strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/connect+json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.NotEqual(t, http.StatusNotFound, resp.StatusCode, ":8080 must route Connect over HTTP/1.1 to the peer-RPC twin")
	require.Contains(t, string(body), `"code"`, "the child must answer the HTTP/1.1 Attach: %s", body)
}

func requireChildUnpublished(t *testing.T, stack *harness.Stack, hostID string) {
	t.Helper()
	ports := loopbackListenPorts(procNetTCP(t, stack, hostID))
	require.NotEmpty(t, ports, "%s published no loopback child listen", hostID)
	for _, port := range ports {
		_, err := stack.ComposeExecOutputTimeout(8*time.Second, "versiond-router",
			"curl", "-m", "2", "-sS", "-o", "/dev/null",
			fmt.Sprintf("http://%s:%d/", hostID, port))
		require.Error(t, err, "child port %d on %s must refuse dials from outside versiond", port, hostID)
	}
}

func requireChildMux(t *testing.T, stack *harness.Stack, hostID string) {
	t.Helper()
	ports := loopbackListenPorts(procNetTCP(t, stack, hostID))
	require.NotEmpty(t, ports)
	// net/http2 dials a second TCP when another request starts before the
	// first child conn is in the pool. That socket stays until
	// IdleConnTimeout (120s). A third connection means RPCs stopped sharing
	// the mux. The admin listen keeps its own connection, so more than one
	// loopback port can be connected.
	const maxChildConns = 2
	connected := 0
	for _, port := range ports {
		n := persistentEstablished(t, stack, hostID, "0100007F", port)
		require.LessOrEqual(t, n, maxChildConns, "%s child port %d has %d persistent connections", hostID, port, n)
		if n >= 1 {
			connected++
		}
	}
	require.GreaterOrEqual(t, connected, 1, "%s must keep an HTTP/2 mux to the child", hostID)
}

func settledPersistentTCP(t *testing.T, stack *harness.Stack, service, remIPHex string, remPort uint16) int {
	t.Helper()
	// Idle JSON hops share versiond:8080 and drop after HAProxy's
	// http-keep-alive (10s). Watch and the per-backend check connection
	// stay. A one-shot health check does not survive the four-sample window.
	deadline := time.Now().Add(15 * time.Second)
	prev, stable := -1, 0
	n := 0
	for {
		n = persistentEstablished(t, stack, service, remIPHex, remPort)
		if n == prev {
			stable++
			if stable >= 2 {
				return n
			}
		} else {
			stable = 0
		}
		prev = n
		if time.Now().After(deadline) {
			return n
		}
		time.Sleep(time.Second)
	}
}

func persistentEstablished(t *testing.T, stack *harness.Stack, service, remIPHex string, remPort uint16) int {
	t.Helper()
	var sets []map[string]struct{}
	for i := 0; i < 4; i++ {
		sets = append(sets, establishedKeys(procNetTCP(t, stack, service), remIPHex, remPort))
		time.Sleep(150 * time.Millisecond)
	}
	keep := sets[0]
	for _, sample := range sets[1:] {
		for key := range keep {
			if _, ok := sample[key]; !ok {
				delete(keep, key)
			}
		}
	}
	return len(keep)
}

func establishedKeys(dump, remIPHex string, remPort uint16) map[string]struct{} {
	out := map[string]struct{}{}
	wantIP := strings.ToUpper(remIPHex)
	for _, line := range strings.Split(dump, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[3] != "01" {
			continue
		}
		parts := strings.Split(strings.ToUpper(fields[2]), ":")
		if len(parts) != 2 {
			continue
		}
		port, err := strconv.ParseUint(parts[1], 16, 16)
		if err != nil || uint16(port) != remPort {
			continue
		}
		if wantIP != "" && parts[0] != wantIP {
			continue
		}
		out[strings.ToUpper(fields[1])+" "+strings.ToUpper(fields[2])] = struct{}{}
	}
	return out
}

func requireOneTCP(t *testing.T, stack *harness.Stack, service, remIPHex string, remPort uint16) {
	t.Helper()
	for i := 0; i < 3; i++ {
		n := countEstablished(procNetTCP(t, stack, service), remIPHex, remPort)
		require.Equal(t, 1, n, "%s established to port %d, sample %d", service, remPort, i)
		time.Sleep(100 * time.Millisecond)
	}
}

func procNetTCP(t *testing.T, stack *harness.Stack, service string) string {
	t.Helper()
	out, err := stack.ComposeExecOutput(service, "sh", "-c", "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null || cat /proc/net/tcp")
	require.NoError(t, err)
	return out
}

func procIPv4(ip string) string {
	p := net.ParseIP(ip).To4()
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%02X%02X%02X%02X", p[3], p[2], p[1], p[0])
}

func countEstablished(dump, remIPHex string, remPort uint16) int {
	n := 0
	wantIP := strings.ToUpper(remIPHex)
	for _, line := range strings.Split(dump, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[3] != "01" {
			continue
		}
		parts := strings.Split(strings.ToUpper(fields[2]), ":")
		if len(parts) != 2 {
			continue
		}
		port, err := strconv.ParseUint(parts[1], 16, 16)
		if err != nil || uint16(port) != remPort {
			continue
		}
		if wantIP != "" && parts[0] != wantIP {
			continue
		}
		n++
	}
	return n
}

func loopbackListenPorts(dump string) []uint16 {
	var out []uint16
	seen := map[uint16]struct{}{}
	for _, line := range strings.Split(dump, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[3] != "0A" {
			continue
		}
		parts := strings.Split(strings.ToUpper(fields[1]), ":")
		if len(parts) != 2 || parts[0] != "0100007F" {
			continue
		}
		port, err := strconv.ParseUint(parts[1], 16, 16)
		if err != nil {
			continue
		}
		p := uint16(port)
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
