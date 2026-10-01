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

// TestPeerRPCOverlayHop is §8.3. The proxy overlay is on, peer RPC is HTTP/2
// on proxy:8443, and :8080 does not answer Connect.
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

	h2addr := stack.ProxyH2HTTP(t)
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(h2addr, "http://"), 5*time.Second)
	require.NoError(t, err, "proxy must publish %s", h2addr)
	_ = conn.Close()

	h2port, err := stack.ComposeExecOutput("devshardctl", "printenv", "DEVSHARD_RPC_H2_PORT")
	require.NoError(t, err)
	require.Equal(t, "8443", strings.TrimSpace(h2port))
	h2host, err := stack.ComposeExecOutput("devshardctl", "printenv", "DEVSHARD_RPC_H2_HOST")
	require.NoError(t, err)
	require.Equal(t, "proxy", strings.TrimSpace(h2host))

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	postGatewayChats(t, client, eps, cfg, "citest overlay hop")
	host, _ := waitHostMetric(t, stack, cfg, 30*time.Second, "devshard_peer_rpc_requests_total", `endpoint="Chat"`, `result="ok"`)
	require.NotEmpty(t, host, "no child counted Chat ok")

	requireOneTCP(t, stack, "devshardctl", "", 8443)
	requireOneTCP(t, stack, "proxy", "", 8081)
	escrowID := config.PrimaryEscrowID(cfg)
	upstream := harness.RequireResponseHeader(t, client, harness.RouterSessionURL(eps.RouterHTTP, version, escrowID, "/healthz"), harness.StickyUpstreamHeader)
	hostID := harness.HostIDForUpstream(cfg, upstream)
	require.NotEmpty(t, hostID, "upstream %q", upstream)
	hostIP := hostIPByID(t, cfg, hostID)
	// The version pool and the host-level pool each keep one proto-h2
	// connection to this listen (RPC mux, and /healthz checks). Concurrent
	// RPCs must ride the version-pool connection, not open another.
	routerConns := settledPersistentTCP(t, stack, "versiond-router", procIPv4(hostIP), 8080)
	require.GreaterOrEqual(t, routerConns, 1)
	require.LessOrEqual(t, routerConns, 2, "versiond-router opened more than one TCP per backend to %s", hostID)
	requireChildMux(t, stack, hostID)

	launchParallelChats(t, client, eps, cfg, 4)
	requireOneTCP(t, stack, "devshardctl", "", 8443)
	requireOneTCP(t, stack, "proxy", "", 8081)
	require.Equal(t, routerConns, settledPersistentTCP(t, stack, "versiond-router", procIPv4(hostIP), 8080),
		"parallel RPCs opened new versiond-router connections to %s", hostID)
	requireChildMux(t, stack, hostID)

	metrics := gatewayMetrics(t, client, eps.GatewayHTTP)
	require.NotContains(t, metrics, "devshard_peer_pool_exhausted_total",
		"HTTP/1.1 pool must not overflow on the h2 mux")

	requireChildUnpublished(t, stack, hostID)
	requireHTTP11NotPeerRPC(t, client, eps.RouterHTTP, version)

	stack.StopService(t, "proxy")
	failClient := &http.Client{Timeout: 20 * time.Second}
	_, err = harness.TryPostGatewayChatCompletion(failClient, eps.GatewayHTTP, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model:     config.PrimaryModelID(cfg),
		Messages:  []harness.ChatMessage{{Role: "user", Content: "citest overlay h2 down"}},
		MaxTokens: 16,
	})
	require.Error(t, err, "closed h2 port must not complete chat on :8080")
}

func requireOverlayRPC(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("DEVSHARD_RPC_SERVER_ENABLED")) != "true" {
		t.Fatal("set DEVSHARD_RPC_SERVER_ENABLED=true (make citest-peerrpc-overlay)")
	}
	if strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_PORT")) == "" {
		t.Fatal("set DEVSHARD_RPC_H2_PORT (make citest-peerrpc-overlay)")
	}
	if strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_HOST")) == "" {
		t.Fatal("set DEVSHARD_RPC_H2_HOST=proxy")
	}
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_UPGRADE")), "true") &&
		strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_UPGRADE")) != "1" {
		t.Fatal("set DEVSHARD_RPC_H2_UPGRADE=true")
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

func requireHTTP11NotPeerRPC(t *testing.T, client *http.Client, router, version string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		router+"/"+version+"/sessions/1/rpc/devshard.transport.v1.PeerAuthService/Attach",
		strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/connect+json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, ":8080 must not answer peer RPC")
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
	mux := 0
	for _, port := range ports {
		n := persistentEstablished(t, stack, hostID, "0100007F", port)
		require.LessOrEqual(t, n, 1, "%s child port %d has %d persistent connections", hostID, port, n)
		if n == 1 {
			mux++
		}
	}
	require.Equal(t, 1, mux, "%s must keep one HTTP/2 connection to the child", hostID)
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
