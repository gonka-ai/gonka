//go:build testenvci

package citest

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"common/httpguard"
	"connectrpc.com/connect"

	"devshard/signing"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/transport"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

// TestPeerRPCParity is §8.4. One overlay stack. The gateway chat is the
// same prompt on JSON (:8080), Connect over HTTP/2, and native gRPC.
// GetSignatures and GetDiffs round-trip on those three dials. A lowered
// child budget then returns resource_exhausted on both HTTP/2 legs.
func TestPeerRPCParity(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	requireOverlayRPC(t)
	if os.Getenv(harness.EnvVersiondImage) != "" || os.Getenv(harness.EnvVersiondRouterImage) != "" {
		t.Fatal("parity is current versiond and versiond-router; unset TESTENV_VERSIOND_IMAGE and TESTENV_VERSIOND_ROUTER_IMAGE")
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DEVSHARD_RPC_GRPC")), "true") {
		t.Fatal("boot with DEVSHARD_RPC_GRPC unset; the test turns native gRPC on for leg P2")
	}
	harness.RequireDocker(t)
	httpguard.SetAllowPrivate(true)

	stack, cfg, eps := harness.BootProxyOverlayStack(t, "citest-peerrpc-parity-*")
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
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	beforeP1 := chatOKCount(t, stack, cfg)
	p1 := captureGatewayChat(t, client, eps, cfg, "citest parity")
	require.Greater(t, chatOKCount(t, stack, cfg), beforeP1, "Connect HTTP/2 chat must be counted on the child")

	// Upgrade off still leaves Connect on :8080, and the current router 404s
	// that path. JSON is an empty gateway endpoint set.
	eps = switchGatewayLeg(t, stack, cfg, client, "false", "false", "")
	beforeH := chatOKCount(t, stack, cfg)
	h := captureGatewayChat(t, client, eps, cfg, "citest parity")
	require.Equal(t, beforeH, chatOKCount(t, stack, cfg), "JSON chat must not use peer RPC")

	eps = switchGatewayLeg(t, stack, cfg, client, "true", "true", peerRPCEndpoints)
	beforeP2 := chatOKCount(t, stack, cfg)
	p2 := captureGatewayChat(t, client, eps, cfg, "citest parity")
	require.Greater(t, chatOKCount(t, stack, cfg), beforeP2, "native gRPC chat must be counted on the child")

	require.Equal(t, h.full, p1.full)
	require.Equal(t, h.full, p2.full)
	require.Equal(t, h.trunc, p1.trunc)
	require.Equal(t, h.trunc, p2.trunc)
	require.NotEmpty(t, h.full.content)
	require.NotEmpty(t, h.trunc.content)
	require.NotEmpty(t, h.trunc.finish)

	compareUnaries(t, stack, cfg, eps, version)
	requireResourceExhaustedOnH2(t, stack, cfg, eps, version)
}

type chatSnap struct {
	content string
	finish  string
	stream  string
	done    bool
}

type chatPair struct {
	full  chatSnap
	trunc chatSnap
}

func captureGatewayChat(t *testing.T, client *http.Client, eps harness.Endpoints, cfg *config.File, prompt string) chatPair {
	t.Helper()
	model := config.PrimaryModelID(cfg)
	fullReq := harness.ChatCompletionRequest{
		Model:     model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: prompt}},
		MaxTokens: 32,
	}
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, fullReq)
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
	stream, done := harness.PostGatewayChatCompletionStream(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, fullReq)
	require.True(t, done, "stream must end with [DONE]")
	require.Equal(t, resp.Choices[0].Message.Content, stream)

	truncReq := fullReq
	truncReq.MaxTokens = 1
	truncResp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, truncReq)
	return chatPair{
		full: chatSnap{
			content: resp.Choices[0].Message.Content,
			finish:  resp.Choices[0].FinishReason,
			stream:  stream,
			done:    done,
		},
		trunc: chatSnap{
			content: truncResp.Choices[0].Message.Content,
			finish:  truncResp.Choices[0].FinishReason,
		},
	}
}

func switchGatewayLeg(t *testing.T, stack *harness.Stack, cfg *config.File, client *http.Client, upgrade, grpc, endpoints string) harness.Endpoints {
	t.Helper()
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_ENDPOINTS", strconv.Quote(endpoints))
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_H2_UPGRADE", strconv.Quote(upgrade))
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_GRPC", strconv.Quote(grpc))
	stack.RecreateServices(t, "devshardctl")
	eps := stack.Endpoints(t, cfg)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	requirePrintenv(t, stack, "DEVSHARD_RPC_H2_UPGRADE", upgrade)
	requirePrintenv(t, stack, "DEVSHARD_RPC_GRPC", grpc)
	requirePrintenv(t, stack, "DEVSHARD_RPC_ENDPOINTS", endpoints)
	return eps
}

func requirePrintenv(t *testing.T, stack *harness.Stack, key, want string) {
	t.Helper()
	out, err := stack.ComposeExecOutput("devshardctl", "printenv", key)
	require.NoError(t, err)
	require.Equal(t, want, strings.TrimSpace(out))
}

func compareUnaries(t *testing.T, stack *harness.Stack, cfg *config.File, eps harness.Endpoints, version string) {
	t.Helper()
	escrow := config.PrimaryEscrowID(cfg)
	signer := userSigner(t, cfg)
	base := eps.RouterHTTP
	proxy := stack.ProxyH2HTTP(t)
	hostAddr := attachableHost(t, base, proxy, escrow, version, signer, cfg)

	httpClient := transport.NewHTTPClient(base, escrow, signer, clientConfig(version, 0))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpSigs, err := httpClient.GetSignatures(ctx, 1)
	require.NoError(t, err)
	httpDiffs, err := httpClient.GetDiffs(ctx, 1, 20)
	require.NoError(t, err)

	p1 := dialPeer(t, base, proxy, escrow, hostAddr, version, signer, false, 0)
	require.NoError(t, p1.WaitReady(ctx))
	p1Sigs, err := p1.GetSignatures(ctx, 1)
	p1Diffs, errDiff := p1.GetDiffs(ctx, 1, 20)
	p1.Close()
	require.NoError(t, err)
	require.NoError(t, errDiff)
	require.Equal(t, normSigs(httpSigs), normSigs(p1Sigs))
	require.Equal(t, diffNonceSigs(httpDiffs), diffNonceSigs(p1Diffs))

	p2 := dialPeer(t, base, proxy, escrow, hostAddr, version, signer, true, 0)
	require.NoError(t, p2.WaitReady(ctx))
	p2Sigs, err := p2.GetSignatures(ctx, 1)
	p2Diffs, errDiff := p2.GetDiffs(ctx, 1, 20)
	p2.Close()
	require.NoError(t, err)
	require.NoError(t, errDiff)
	require.Equal(t, normSigs(httpSigs), normSigs(p2Sigs))
	require.Equal(t, diffNonceSigs(httpDiffs), diffNonceSigs(p2Diffs))
}

func requireResourceExhaustedOnH2(t *testing.T, stack *harness.Stack, cfg *config.File, eps harness.Endpoints, version string) {
	t.Helper()
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_MSGS_PER_MIN", strconv.Quote("10"))
	var hosts []string
	for _, h := range cfg.Hosts {
		hosts = append(hosts, h.ID)
	}
	stack.RecreateServices(t, hosts...)
	harness.WaitGETOK(t, harness.GatewayChatClient(), eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health after budget drop", stack)

	escrow := config.PrimaryEscrowID(cfg)
	signer := userSigner(t, cfg)
	base := eps.RouterHTTP
	proxy := stack.ProxyH2HTTP(t)
	hostAddr := attachableHost(t, base, proxy, escrow, version, signer, cfg)
	warm := dialPeer(t, base, proxy, escrow, hostAddr, version, signer, false, 0)
	warmCtx, warmCancel := context.WithTimeout(context.Background(), time.Minute)
	require.NoError(t, warm.WaitReady(warmCtx))
	_, warmErr := warm.GetSignatures(warmCtx, 1)
	warmCancel()
	warm.Close()
	require.NoError(t, warmErr, "one GetSignatures must fit in the burst")

	for _, grpc := range []bool{false, true} {
		rpc := dialPeer(t, base, proxy, escrow, hostAddr, version, signer, grpc, 2*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		require.NoError(t, rpc.WaitReady(ctx), "grpc=%v", grpc)
		_, err := rpc.GetSignatures(ctx, 1)
		rpc.Close()
		cancel()
		require.Error(t, err, "grpc=%v", grpc)
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err), "grpc=%v: %v", grpc, err)
	}
}

func clientConfig(version string, queryTimeout time.Duration) transport.ClientConfig {
	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/devshard/" + version
	if queryTimeout > 0 {
		cfg.QueryTimeout = queryTimeout
	}
	return cfg
}

func dialPeer(t *testing.T, base, proxy, escrow, hostAddr, version string, signer signing.Signer, grpc bool, queryTimeout time.Duration) *transport.RPCClient {
	t.Helper()
	hc := transport.NewHTTPClient(base, escrow, signer, clientConfig(version, queryTimeout))
	pc := transport.NewPeerConn(transport.PeerConnConfig{
		BaseURL:      base,
		RoutePrefix:  "/devshard/" + version,
		DoorEscrowID: escrow,
		HostAddress:  hostAddr,
		Signer:       signer,
		DialSet: transport.PeerRPCDialSet{
			InferenceURL: base,
			H2URL:        proxy,
		},
		GRPC: grpc,
	})
	rpc := transport.NewRPCClient(hc, pc, transport.ParseRPCEndpoints(peerRPCEndpoints))
	pc.Start()
	return rpc
}

func userSigner(t *testing.T, cfg *config.File) signing.Signer {
	t.Helper()
	signer, err := signing.SignerFromHex(cfg.User.PrivateKeyHex)
	require.NoError(t, err)
	return signer
}

// attachableHost is the child address whose Attach accepts this signer.
// The escrow hash picks one versiond; the other address is rejected.
func attachableHost(t *testing.T, base, proxy, escrow, version string, signer signing.Signer, cfg *config.File) string {
	t.Helper()
	var errs []string
	for _, h := range cfg.Hosts {
		rpc := dialPeer(t, base, proxy, escrow, h.Address, version, signer, false, 0)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err := rpc.WaitReady(ctx)
		cancel()
		rpc.Close()
		if err == nil {
			return h.Address
		}
		errs = append(errs, h.ID+": "+err.Error())
	}
	t.Fatalf("no host accepted Attach: %s", strings.Join(errs, "; "))
	return ""
}

func normSigs(in map[uint32][]byte) map[uint32][]byte {
	if in == nil {
		return map[uint32][]byte{}
	}
	return in
}

type nonceSig struct {
	nonce uint64
	sig   string
}

func diffNonceSigs(diffs []types.Diff) []nonceSig {
	out := make([]nonceSig, len(diffs))
	for i, d := range diffs {
		out[i] = nonceSig{nonce: d.Nonce, sig: string(d.UserSig)}
	}
	return out
}

func chatOKCount(t *testing.T, stack *harness.Stack, cfg *config.File) float64 {
	t.Helper()
	var sum float64
	for _, body := range hostMetricBodies(t, stack, cfg) {
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !strings.Contains(line, "devshard_peer_rpc_requests_total") || !strings.Contains(line, `endpoint="Chat"`) || !strings.Contains(line, `result="ok"`) {
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
