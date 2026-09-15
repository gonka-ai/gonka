package harness

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestRewriteRPCProxyForTestKeepsIndependentZones(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "proxy", "haproxy-rpc.cfg"))
	require.NoError(t, err)
	got := rewriteRPCProxyForTest(string(src), "versiond-router:19081", defaultRPCProxyTestLimits())
	require.Contains(t, got, "bind *:8443 proto h2")
	require.Contains(t, got, "http-request del-header X-Real-IP")
	require.Contains(t, got, "X-Real-IP %[src]")
	require.Contains(t, got, "path_end /devshard.transport.v1.PeerAuthService/Attach")
	require.Contains(t, got, "path_end /devshard.transport.v1.SessionService/GetDiffs")
	require.Contains(t, got, "track-sc1 src table st_rpc_attach")
	require.Contains(t, got, "track-sc1 src table st_rpc_chat")
	require.Contains(t, got, "if is_diffs { sc_http_req_rate(1) gt 3 }")
	require.Contains(t, got, "if is_attach { sc_http_req_rate(1) gt 2 }")
	require.Contains(t, got, "server router versiond-router:19081\n")
	require.NotContains(t, got, "server router versiond-router:19081 proto h2")
	require.NotContains(t, got, "server router versiond-router:19081 resolvers docker")
	// Production mempool ceiling stays; only GetDiffs is rewritten.
	require.Contains(t, got, "if is_mempool { sc_http_req_rate(1) gt 400 }")
}

func TestProxyHAProxy_GetDiffsFloodDoesNotStarveChat(t *testing.T) {
	fx := startRPCProxyHAProxy(t, defaultRPCProxyTestLimits())
	client := newH2CClient(t)

	var lastDiffs int
	for i := 0; i < 8; i++ {
		lastDiffs = mustProxyRPCStatus(t, client, fx.url, "/devshard.transport.v1.SessionService/GetDiffs")
		if lastDiffs == http.StatusTooManyRequests {
			break
		}
	}
	require.Equal(t, http.StatusTooManyRequests, lastDiffs, "GetDiffs path zone must trip")
	require.Greater(t, fx.diffs.Load(), int32(0))
	require.LessOrEqual(t, fx.diffs.Load(), int32(defaultRPCProxyTestLimits().DiffsRate))

	require.Equal(t, http.StatusOK, mustProxyRPCStatus(t, client, fx.url, "/devshard.transport.v1.SessionService/Chat"))
	require.Equal(t, int32(1), fx.chat.Load(), "Chat table is independent of GetDiffs")
}

func TestProxyHAProxy_AttachFloodNeverHitsBackend(t *testing.T) {
	fx := startRPCProxyHAProxy(t, defaultRPCProxyTestLimits())
	client := newH2CClient(t)

	ok := 0
	denied := 0
	for i := 0; i < 8; i++ {
		code := mustProxyRPCStatus(t, client, fx.url, "/devshard.transport.v1.PeerAuthService/Attach")
		switch code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			denied++
		default:
			t.Fatalf("Attach status %d", code)
		}
	}
	require.Greater(t, denied, 0, "Attach path zone must refuse the flood")
	require.Equal(t, int32(ok), fx.attach.Load(), "HAProxy deny must not reach the backend (ECDSA)")
	require.LessOrEqual(t, fx.attach.Load(), int32(defaultRPCProxyTestLimits().AttachRate))
}

func TestProxyHAProxy_XRealIPOverwrittenFromSrc(t *testing.T) {
	fx := startRPCProxyHAProxy(t, defaultRPCProxyTestLimits())
	client := newH2CClient(t)
	req, err := http.NewRequest(http.MethodPost, fx.url+"/devshard.transport.v1.SessionService/Chat", nil)
	require.NoError(t, err)
	req.Header.Set("X-Real-IP", "203.0.113.9")
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := fx.lastRealIP.Load()
	require.NotNil(t, got)
	require.NotEqual(t, "203.0.113.9", got, "caller X-Real-IP must be deleted")
	require.NotEmpty(t, got)
}

func TestProxyHAProxy_SecondSrcNotThrottled(t *testing.T) {
	RequireDocker(t)
	lim := defaultRPCProxyTestLimits()
	lim.AttachRate = 2
	fx := startRPCProxyHAProxyOnNetwork(t, lim)

	a := startCurlPeer(t, fx.network)
	b := startCurlPeer(t, fx.network)

	codeA := a.flood(t, fx.alias, "/devshard.transport.v1.PeerAuthService/Attach", 6)
	require.Contains(t, codeA, "429", "first src must hit the Attach zone")

	codeB := b.flood(t, fx.alias, "/devshard.transport.v1.PeerAuthService/Attach", 1)
	require.Contains(t, codeB, "200", "second src must not share the first src's stick-table, got %q", codeB)
}

func TestProxyHAProxy_ProductionCfgSyntax(t *testing.T) {
	RequireDocker(t)
	cfg, err := filepath.Abs(filepath.Join("..", "..", "proxy", "haproxy-rpc.cfg"))
	require.NoError(t, err)
	cmd := exec.Command("docker", "run", "--rm",
		"-v", cfg+":/usr/local/etc/haproxy/haproxy.cfg:ro",
		"haproxy:3.2-alpine", "haproxy", "-c", "-f", "/usr/local/etc/haproxy/haproxy.cfg")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

type rpcProxyFixture struct {
	url        string
	alias      string
	network    string
	attach     atomic.Int32
	diffs      atomic.Int32
	chat       atomic.Int32
	lastRealIP atomic.Value
}

func startRPCProxyHAProxy(t *testing.T, lim rpcProxyTestLimits) *rpcProxyFixture {
	t.Helper()
	return startRPCProxyHAProxyOpt(t, lim, false)
}

func startRPCProxyHAProxyOnNetwork(t *testing.T, lim rpcProxyTestLimits) *rpcProxyFixture {
	t.Helper()
	return startRPCProxyHAProxyOpt(t, lim, true)
}

func startRPCProxyHAProxyOpt(t *testing.T, lim rpcProxyTestLimits, userNet bool) *rpcProxyFixture {
	t.Helper()
	RequireDocker(t)

	fx := &rpcProxyFixture{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.lastRealIP.Store(r.Header.Get("X-Real-IP"))
		switch {
		case strings.HasSuffix(r.URL.Path, "PeerAuthService/Attach"):
			fx.attach.Add(1)
		case strings.HasSuffix(r.URL.Path, "SessionService/GetDiffs"):
			fx.diffs.Add(1)
		case strings.HasSuffix(r.URL.Path, "SessionService/Chat"):
			fx.chat.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	})
	backend := httptest.NewUnstartedServer(handler)
	require.NoError(t, backend.Listener.Close())
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	require.NoError(t, err)
	backend.Listener = ln
	backend.Start()
	t.Cleanup(backend.Close)

	u, err := url.Parse(backend.URL)
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)

	src, err := os.ReadFile(filepath.Join("..", "..", "proxy", "haproxy-rpc.cfg"))
	require.NoError(t, err)
	// host-gateway extra_hosts prefer IPv6 on Docker Desktop; HAProxy then
	// cannot reach a tcp4 httptest. Pin the IPv4 gateway in the server line.
	backendAddr := net.JoinHostPort(dockerHostIPv4(t), port)
	cfg := rewriteRPCProxyForTest(string(src), backendAddr, lim)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "haproxy.cfg"), []byte(cfg), 0o644))

	name := fmt.Sprintf("p67-haproxy-%d", time.Now().UnixNano())
	args := []string{"run", "-d", "--name", name,
		"-v", dir + ":/usr/local/etc/haproxy:ro",
	}
	if userNet {
		netName := fmt.Sprintf("p67-net-%d", time.Now().UnixNano())
		out, err := exec.Command("docker", "network", "create", netName).CombinedOutput()
		require.NoError(t, err, "%s", out)
		t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", netName).Run() })
		fx.network = netName
		fx.alias = "proxy"
		args = append(args, "--network", netName, "--network-alias", "proxy")
	} else {
		args = append(args, "-p", "127.0.0.1::8443")
	}
	args = append(args, "haproxy:3.2-alpine")
	out, err := exec.Command("docker", args...).CombinedOutput()
	require.NoError(t, err, "%s", out)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	if userNet {
		fx.url = "http://proxy:8443"
		peer := startCurlPeer(t, fx.network)
		waitHAProxyReady(t, name, func() (int, error) {
			got := peer.flood(t, fx.alias, "/devshard.transport.v1.SessionService/Chat", 1)
			if strings.Contains(got, "200") {
				return http.StatusOK, nil
			}
			return 0, fmt.Errorf("curl: %s", strings.TrimSpace(got))
		})
		fx.chat.Store(0)
		return fx
	}

	portOut, err := exec.Command("docker", "port", name, "8443/tcp").Output()
	require.NoError(t, err, "%s", portOut)
	mapped, err := publishedLocalPort(string(portOut))
	require.NoError(t, err)
	fx.url = "http://127.0.0.1:" + mapped

	client := newH2CClient(t)
	waitHAProxyReady(t, name, func() (int, error) {
		return proxyRPCStatus(client, fx.url, "/devshard.transport.v1.SessionService/Chat")
	})
	fx.chat.Store(0)
	return fx
}

func waitHAProxyReady(t *testing.T, container string, probe func() (int, error)) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastCode int
	var lastErr error
	for time.Now().Before(deadline) {
		lastCode, lastErr = probe()
		if lastErr == nil && lastCode == http.StatusOK {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	logs, _ := exec.Command("docker", "logs", container).CombinedOutput()
	t.Fatalf("haproxy not ready code=%d err=%v\n%s", lastCode, lastErr, logs)
}

func publishedLocalPort(portOut string) (string, error) {
	for _, line := range strings.Split(strings.TrimSpace(portOut), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "127.0.0.1:"):
			return strings.TrimPrefix(line, "127.0.0.1:"), nil
		case strings.HasPrefix(line, "0.0.0.0:"):
			return strings.TrimPrefix(line, "0.0.0.0:"), nil
		}
	}
	return "", fmt.Errorf("docker port: %q", portOut)
}

func newH2CClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func proxyRPCStatus(client *http.Client, base, path string) (int, error) {
	req, err := http.NewRequest(http.MethodPost, base+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func mustProxyRPCStatus(t *testing.T, client *http.Client, base, path string) int {
	t.Helper()
	code, err := proxyRPCStatus(client, base, path)
	require.NoError(t, err)
	return code
}

type curlPeer struct {
	name string
}

func startCurlPeer(t *testing.T, network string) *curlPeer {
	t.Helper()
	name := fmt.Sprintf("p67-curl-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--name", name,
		"--network", network, "--entrypoint", "sleep",
		"curlimages/curl:8.11.1", "120").CombinedOutput()
	require.NoError(t, err, "%s", out)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	return &curlPeer{name: name}
}

func (p *curlPeer) flood(t *testing.T, alias, path string, n int) string {
	t.Helper()
	target := "http://" + alias + ":8443" + path
	var b strings.Builder
	for i := 0; i < n; i++ {
		cmd := exec.Command("docker", "exec", p.name,
			"curl", "-sS", "-o", "/dev/null", "-w", "%{http_code} ",
			"--http2-prior-knowledge", "-X", "POST", target)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("curl: %v %s", err, out)
		}
		b.Write(out)
	}
	return b.String()
}

var (
	dockerHostIPv4Once sync.Once
	dockerHostIPv4Val  string
	dockerHostIPv4Err  error
)

func dockerHostIPv4(t *testing.T) string {
	t.Helper()
	dockerHostIPv4Once.Do(func() {
		out, err := exec.Command("docker", "run", "--rm",
			"--add-host=gw:host-gateway",
			"haproxy:3.2-alpine", "getent", "ahostsv4", "gw").CombinedOutput()
		if err != nil {
			dockerHostIPv4Err = fmt.Errorf("host-gateway ipv4: %v (%s)", err, out)
			return
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			ip := net.ParseIP(fields[0])
			if ip != nil && ip.To4() != nil {
				dockerHostIPv4Val = ip.String()
				return
			}
		}
		dockerHostIPv4Err = fmt.Errorf("no ipv4 in getent ahostsv4: %s", out)
	})
	require.NoError(t, dockerHostIPv4Err)
	require.NotEmpty(t, dockerHostIPv4Val)
	return dockerHostIPv4Val
}
