//go:build testenvci

package citest

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"common/httpguard"
	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/signing"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/transport"
	"devshard/transport/rpcpb"

	"github.com/stretchr/testify/require"
)

const (
	limitMsgsPerMin = "600"
	limitBurst      = "80"
	limitFloor      = "200"
	baselineVersion = "devshard-versiond:0.2.15-v5"
	baselineRouter  = "devshard-versiond-router:0.2.15-v5"
)

// TestPeerRPCLimitsNoProxy is §9 R8 (defaults), R1, and R2 on the 0.2.15-v5
// pin. Peer RPC is Connect over HTTP/1.1. There is no proxy zone.
func TestPeerRPCLimitsNoProxy(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	requireImage(t, baselineVersion)
	requireImage(t, baselineRouter)
	t.Setenv(harness.EnvVersiondImage, baselineVersion)
	t.Setenv(harness.EnvVersiondRouterImage, baselineRouter)
	t.Setenv("DEVSHARD_RPC_SERVER_ENABLED", "true")
	t.Setenv("DEVSHARD_RPC_ENDPOINTS", peerRPCEndpoints)
	t.Setenv("DEVSHARD_RPC_H2_PORT", "")
	t.Setenv("DEVSHARD_RPC_H2_HOST", "")
	t.Setenv("DEVSHARD_RPC_H2_UPGRADE", "")
	t.Setenv("TESTENV_PROXY_OVERLAY", "")
	t.Setenv("DEVSHARD_RPC_MSGS_PER_MIN", "")
	t.Setenv("DEVSHARD_RPC_MSGS_BURST", "")
	t.Setenv("DEVSHARD_RPC_ATTACH_PER_MIN_TOTAL", "")
	httpguard.SetAllowPrivate(true)

	stack, cfg, eps := harness.BootStack(t, "citest-peerrpc-limits-noproxy-*")
	version := stackVersion(cfg)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGETOK(t, harness.GatewayChatClient(), eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health", stack)

	escrow := config.PrimaryEscrowID(cfg)
	user := userSigner(t, cfg)
	peerB := signerHex(t, cfg.Hosts[0].PrivateKeyHex)

	t.Log("R8 no-proxy: a few reads at the default budget are not throttled")
	rpc, _ := openReady(t, eps.RouterHTTP, "", escrow, version, user, cfg, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	for i := 0; i < 3; i++ {
		_, err := rpc.GetSignatures(ctx, 1)
		require.NoError(t, err)
	}
	cancel()
	rpc.Close()

	stack.StopService(t, "devshardctl")
	patchChildLimits(t, stack)
	recreateHosts(t, stack, cfg)
	harness.WaitGETOK(t, harness.GatewayChatClient(), eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health after limit drop", stack)

	t.Log("R1: GetDiffs flood exhausts one peer; Chat still fits; the other peer does not")
	proveChildPie(t, eps.RouterHTTP, "", escrow, version, user, peerB, cfg)

	t.Log("R2: oversized Attach skips ECDSA; the floor reject does too")
	proveAttachFloor(t, stack, cfg, eps.RouterHTTP, version, escrow, false)
}

// TestPeerRPCLimitsOverlay is §9 R8 plus R3–R7 on current images through proxy.
func TestPeerRPCLimitsOverlay(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	if os.Getenv(harness.EnvVersiondImage) != "" || os.Getenv(harness.EnvVersiondRouterImage) != "" {
		t.Fatal("overlay limits use current versiond and versiond-router; unset TESTENV_VERSIOND_IMAGE and TESTENV_VERSIOND_ROUTER_IMAGE")
	}
	t.Setenv("DEVSHARD_RPC_SERVER_ENABLED", "true")
	t.Setenv("DEVSHARD_RPC_ENDPOINTS", peerRPCEndpoints)
	t.Setenv("DEVSHARD_RPC_H2_PORT", "8443")
	t.Setenv("DEVSHARD_RPC_H2_HOST", "proxy")
	t.Setenv("DEVSHARD_RPC_H2_UPGRADE", "true")
	t.Setenv("DEVSHARD_RPC_MSGS_PER_MIN", "")
	t.Setenv("DEVSHARD_RPC_MSGS_BURST", "")
	t.Setenv("DEVSHARD_RPC_ATTACH_PER_MIN_TOTAL", "")
	httpguard.SetAllowPrivate(true)

	stack, cfg, eps := harness.BootProxyOverlayStack(t, "citest-peerrpc-limits-overlay-*")
	version := stackVersion(cfg)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGETOK(t, harness.GatewayChatClient(), eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health", stack)
	proxy := stack.ProxyH2HTTP(t)

	escrow := config.PrimaryEscrowID(cfg)
	user := userSigner(t, cfg)
	peerB := signerHex(t, cfg.Hosts[0].PrivateKeyHex)

	t.Log("R8 overlay: production proxy ceilings do not throttle a few reads")
	rpc, _ := openReady(t, eps.RouterHTTP, proxy, escrow, version, user, cfg, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	_, err := rpc.GetSignatures(ctx, 1)
	require.NoError(t, err)
	_, err = rpc.GetDiffs(ctx, 1, 20)
	require.NoError(t, err)
	cancel()
	rpc.Close()

	stack.StopService(t, "devshardctl")
	patchChildLimits(t, stack)
	recreateHosts(t, stack, cfg)
	harness.WaitGETOK(t, harness.GatewayChatClient(), eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health after limit drop", stack)

	t.Log("R3/R7: child pie matches R1, and the GetDiffs zone is independent of Chat")
	proveChildPie(t, eps.RouterHTTP, proxy, escrow, version, user, peerB, cfg)
	// Attach stays above the child floor (200) so the flood reaches ECDSA.
	// The Attach zone is lowered only after that proof.
	harness.LowerProxyRPCRates(t, stack, 200, 1000, 3, 50)
	proxy = recreateProxy(t, stack)
	proveDiffsZone(t, proxy, version, escrow)

	t.Log("R4: child floor, then the Attach zone refuses before the child")
	proveAttachFloor(t, stack, cfg, proxy, version, escrow, true)
	harness.LowerProxyRPCRates(t, stack, 200, 2, 3, 50)
	proxy = recreateProxy(t, stack)
	t.Log("R6: a spoofed X-Real-IP does not buy a new src bucket")
	proveSpoofedIPSharesSrc(t, proxy, version, escrow)
	proveAttachZone(t, stack, cfg, proxy, version, escrow)

	t.Log("R5: conn_rate rejects one src before Attach; a second src still connects")
	harness.LowerProxyRPCRates(t, stack, 3, 2, 3, 50)
	proxy = recreateProxy(t, stack)
	proveConnRate(t, stack, proxy)
}

func recreateProxy(t *testing.T, stack *harness.Stack) string {
	t.Helper()
	stack.RecreateServices(t, "proxy")
	time.Sleep(time.Second)
	return stack.ProxyH2HTTP(t)
}

func proveChildPie(t *testing.T, base, proxy, escrow, version string, peerA, peerB signing.Signer, cfg *config.File) {
	t.Helper()
	rpc, hostAddr := openReady(t, base, proxy, escrow, version, peerA, cfg, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := rpc.GetDiffs(ctx, 1, 20)
	require.NoError(t, err, "first GetDiffs fits in the burst")
	var buf bytes.Buffer
	_, chatErr := rpc.Send(ctx, host.HostRequest{Nonce: 1}, &buf, nil)
	require.False(t, isChildRateLimit(chatErr), "Chat must still fit in the remaining weight, got %v", chatErr)
	if chatErr != nil {
		require.NotEqual(t, connect.CodePermissionDenied, connect.CodeOf(chatErr), "Chat must be admitted: %v", chatErr)
	}
	rpc.Close()

	// A second Attach for the same signer replaces the live session. Open it
	// only after the first client is released, on the host that already accepted.
	short := dialPeer(t, base, proxy, escrow, hostAddr, version, peerA, false, time.Second)
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 20*time.Second)
	require.NoError(t, short.WaitReady(readyCtx))
	readyCancel()
	_, err = short.GetDiffs(ctx, 1, 20)
	short.Close()
	require.True(t, isChildRateLimit(err), "second GetDiffs on the same peer: %v", err)

	other, _ := openReady(t, base, proxy, escrow, version, peerB, cfg, 0)
	_, err = other.GetDiffs(ctx, 1, 20)
	other.Close()
	require.NoError(t, err, "second peer has its own budget")
}

func proveDiffsZone(t *testing.T, proxy, version, escrow string) {
	t.Helper()
	diffs := 0
	chatOK := false
	for i := 0; i < 8; i++ {
		code, body := postProcedure(t, true, proxy, version, escrow, "/devshard.transport.v1.SessionService/GetDiffs", []byte("x"))
		if code == http.StatusTooManyRequests {
			diffs++
			break
		}
		if i == 7 {
			t.Fatalf("GetDiffs zone did not trip, last %d %s", code, body)
		}
	}
	require.Greater(t, diffs, 0)
	code, body := postProcedure(t, true, proxy, version, escrow, "/devshard.transport.v1.SessionService/Chat", []byte("x"))
	chatOK = code != http.StatusTooManyRequests
	require.True(t, chatOK, "Chat zone is independent of GetDiffs, got %d %s", code, body)
}

func proveAttachFloor(t *testing.T, stack *harness.Stack, cfg *config.File, base, version, escrow string, h2 bool) {
	t.Helper()
	// One HTTP/2 connection for the flood. A fresh dial per Attach trips the
	// proxy conn_rate (200/s) and the client sees unexpected EOF.
	var shared *http.Client
	if h2 {
		shared = h2cClient()
	}
	post := func(payload []byte) (int, string) {
		t.Helper()
		code, body, err := doProcedure(shared, h2, base, version, escrow, "/devshard.transport.v1.PeerAuthService/Attach", payload, "")
		if err != nil && h2 && transientPost(err) {
			shared = h2cClient()
			code, body, err = doProcedure(shared, h2, base, version, escrow, "/devshard.transport.v1.PeerAuthService/Attach", payload, "")
		}
		require.NoError(t, err)
		return code, body
	}

	before := metricSum(t, stack, cfg, "devshard_peer_rpc_attach_total", `result="unauthenticated"`)
	code, body := post(bytes.Repeat([]byte("x"), 4097))
	require.Contains(t, body, "attach request too large", "status %d body %s", code, body)
	require.Equal(t, before, metricSum(t, stack, cfg, "devshard_peer_rpc_attach_total", `result="unauthenticated"`),
		"oversized Attach must not reach ECDSA")

	hostAddr := ""
	for _, h := range cfg.Hosts {
		_, body = post(badAttach(t, h.Address))
		if strings.Contains(body, "host_address does not match") {
			continue
		}
		hostAddr = h.Address
		break
	}
	require.NotEmpty(t, hostAddr, "no child accepted host_address, last %s", body)
	require.Contains(t, body, "recover address", "bad signature must reach ECDSA, got %s", body)
	after := metricSum(t, stack, cfg, "devshard_peer_rpc_attach_total", `result="unauthenticated"`)
	require.Greater(t, after, before, "ECDSA failure must count as unauthenticated")

	ecdsa := 0
	for i := 0; i < 250; i++ {
		_, body = post(badAttach(t, hostAddr))
		if strings.Contains(body, "too many attach attempts") {
			floor := metricSum(t, stack, cfg, "devshard_peer_rpc_attach_total", `result="unauthenticated"`)
			require.Equal(t, after+float64(ecdsa), floor, "floor reject must not run ECDSA, body %s", body)
			return
		}
		require.Contains(t, body, "recover address", "expected ECDSA before the floor, body %s", body)
		ecdsa++
	}
	t.Fatalf("attach floor did not fire, last body %s", body)
}

func proveAttachZone(t *testing.T, stack *harness.Stack, cfg *config.File, proxy, version, escrow string) {
	t.Helper()
	denied := 0
	var frozen float64
	seenDeny := false
	for i := 0; i < 12; i++ {
		code, body := postProcedure(t, true, proxy, version, escrow, "/devshard.transport.v1.PeerAuthService/Attach", badAttach(t, cfg.Hosts[0].Address))
		if code != http.StatusTooManyRequests {
			continue
		}
		if !seenDeny {
			frozen = metricSum(t, stack, cfg, "devshard_peer_rpc_attach_total", `result="resource_exhausted"`)
			seenDeny = true
			continue
		}
		denied++
		require.Equal(t, frozen, metricSum(t, stack, cfg, "devshard_peer_rpc_attach_total", `result="resource_exhausted"`),
			"proxy 429 must not reach the child floor, body %s", body)
	}
	require.True(t, seenDeny, "Attach path zone must 429")
	require.Greater(t, denied, 0, "Attach path zone must keep refusing")
}

func proveConnRate(t *testing.T, stack *harness.Stack, proxy string) {
	t.Helper()
	addr := strings.TrimPrefix(proxy, "http://")
	refused := 0
	for i := 0; i < 12; i++ {
		if !tcpAccepted(addr) {
			refused++
		}
	}
	require.Greater(t, refused, 0, "conn_rate must refuse the flooding src before HTTP")

	netName := containerNetwork(t, stack.ComposeProject+"-proxy-1")
	cmd := exec.Command("docker", "run", "--rm", "--network", netName, "alpine:3.21", "nc", "-z", "-w", "2", "proxy", "8443")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "second src must still connect: %s", out)
}

func proveSpoofedIPSharesSrc(t *testing.T, proxy, version, escrow string) {
	t.Helper()
	denied := 0
	for i := 0; i < 8; i++ {
		code, _ := postProcedureHeader(t, true, proxy, version, escrow, "/devshard.transport.v1.PeerAuthService/Attach", badAttach(t, "gonka1badhost"), "203.0.113."+strconv.Itoa(i+1))
		if code == http.StatusTooManyRequests {
			denied++
		}
	}
	require.Greater(t, denied, 0, "unique X-Real-IP values from one src must still share the Attach zone")
}

func patchChildLimits(t *testing.T, stack *harness.Stack) {
	t.Helper()
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_MSGS_PER_MIN", `"`+limitMsgsPerMin+`"`)
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_MSGS_BURST", `"`+limitBurst+`"`)
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_ATTACH_PER_MIN_TOTAL", `"`+limitFloor+`"`)
}

func recreateHosts(t *testing.T, stack *harness.Stack, cfg *config.File) {
	t.Helper()
	var ids []string
	for _, h := range cfg.Hosts {
		ids = append(ids, h.ID)
	}
	stack.RecreateServices(t, ids...)
}

func openReady(t *testing.T, base, proxy, escrow, version string, signer signing.Signer, cfg *config.File, query time.Duration) (*transport.RPCClient, string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	last := make(map[string]string, len(cfg.Hosts))
	for time.Now().Before(deadline) {
		for _, h := range cfg.Hosts {
			rpc := dialPeer(t, base, proxy, escrow, h.Address, version, signer, false, query)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			err := rpc.WaitReady(ctx)
			cancel()
			if err == nil {
				return rpc, h.Address
			}
			rpc.Close()
			last[h.ID] = err.Error()
		}
	}
	parts := make([]string, 0, len(last))
	for id, err := range last {
		parts = append(parts, id+": "+err)
	}
	t.Fatalf("no host accepted Attach: %s", strings.Join(parts, "; "))
	return nil, ""
}

func postProcedure(t *testing.T, h2 bool, base, version, escrow, procedure string, body []byte) (int, string) {
	t.Helper()
	return postProcedureHeader(t, h2, base, version, escrow, procedure, body, "")
}

func postProcedureHeader(t *testing.T, h2 bool, base, version, escrow, procedure string, body []byte, spoof string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	if h2 {
		client = h2cClient()
	}
	code, raw, err := doProcedure(client, h2, base, version, escrow, procedure, body, spoof)
	if err != nil && h2 && transientPost(err) {
		code, raw, err = doProcedure(h2cClient(), h2, base, version, escrow, procedure, body, spoof)
	}
	require.NoError(t, err)
	return code, raw
}

func doProcedure(client *http.Client, h2 bool, base, version, escrow, procedure string, body []byte, spoof string) (int, string, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
		if h2 {
			client = h2cClient()
		}
	}
	url := strings.TrimRight(base, "/") + "/devshard/" + version + "/sessions/" + escrow + "/rpc" + procedure
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	if spoof != "" {
		req.Header.Set("X-Real-IP", spoof)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(raw), nil
}

func transientPost(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unexpected EOF") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "server closed idle connection")
}

func h2cClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

func badAttach(t *testing.T, hostAddr string) []byte {
	t.Helper()
	msg := &rpcpb.AttachRequest{
		PeerAddress:     "gonka1badpeer",
		HostAddress:     hostAddr,
		AttachNonce:     []byte("0123456789abcdef0123"),
		ProtocolVersion: transport.AttachProtocolVersion,
		Timestamp:       time.Now().Unix(),
		Signature:       bytes.Repeat([]byte{1}, 64),
	}
	raw, err := proto.Marshal(msg)
	require.NoError(t, err)
	return raw
}

func tcpAccepted(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(300 * time.Millisecond))
	_, err = c.Write([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"))
	if err != nil {
		return false
	}
	buf := make([]byte, 8)
	_, err = c.Read(buf)
	return err == nil
}

func containerNetwork(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", `{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}`, name).CombinedOutput()
	require.NoError(t, err, "%s", out)
	netName := strings.TrimSpace(string(out))
	require.NotEmpty(t, netName)
	return netName
}

func metricSum(t *testing.T, stack *harness.Stack, cfg *config.File, name string, parts ...string) float64 {
	t.Helper()
	var sum float64
	for _, body := range hostMetricBodies(t, stack, cfg) {
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, name) {
				continue
			}
			ok := true
			for _, part := range parts {
				if !strings.Contains(line, part) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			fields := strings.Fields(line)
			v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
			require.NoError(t, err)
			sum += v
		}
	}
	return sum
}

func isChildRateLimit(err error) bool {
	return err != nil && connect.CodeOf(err) == connect.CodeResourceExhausted && strings.Contains(err.Error(), "rate limit exceeded")
}

func signerHex(t *testing.T, hexKey string) signing.Signer {
	t.Helper()
	s, err := signing.SignerFromHex(hexKey)
	require.NoError(t, err)
	return s
}

func stackVersion(cfg *config.File) string {
	if cfg.Versiond.VersionName == "" {
		return "v2"
	}
	return cfg.Versiond.VersionName
}

func requireImage(t *testing.T, image string) {
	t.Helper()
	out, err := exec.Command("docker", "image", "inspect", image).CombinedOutput()
	require.NoError(t, err, "missing %s: %s", image, out)
}
